// Package codex 包装 OpenAI 的 codex-computer-use.exe，实现 engine.Engine。
//
// 协议（已实测确认）：
//
//	启动：<exe> --parent-pid <pid>，stdio 管道
//	请求：{"id":N,"method":"...","params":{...},"meta":{...}}\n     （换行分隔 JSON）
//	响应：{"id":N,"ok":bool,"result":?,"error":?,"approvalRequest":?}
//
// 注意响应可能耗时约 10 秒，不可当作卡死。
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhilv666/cua-mcp/internal/engine"
)

const (
	// defaultTimeout 与上游一致：普通请求 10s。
	defaultTimeout = 10 * time.Second
	// launchAppTimeout 拉起应用更慢，上游给 15s。
	launchAppTimeout = 15 * time.Second

	// approvedAppMetaKey 是引擎用来判断「这个 app 已获批准」的元数据键。
	approvedAppMetaKey = "x-oai-cua-approved-app"
	// requestBudgetMetaKey 告诉引擎本次请求允许多久。
	requestBudgetMetaKey = "x-oai-cua-request-budget-ms"

	// exitInterrupted 是引擎被用户按物理 Escape 中止时的退出码。
	// 上游据此把错误换成「用户已停止 Computer Use」的提示。
	exitInterrupted = 130
)

// ErrInterrupted 表示用户按物理 Escape 中止了 Computer Use。
//
// 引擎自己监听 Escape（闭源二进制里的键盘钩子），触发时以退出码 130 结束。
// 上游收到这个码会告诉模型：停下，本轮不要再调 Computer Use。
var ErrInterrupted = errors.New(
	"Computer Use 已被用户按物理 Escape 键中止。请停止当前工作，" +
		"本轮不要再调用 Computer Use 工具，并向用户说明是用户中止了操作。")

// response 是 helper 的一行响应。
type response struct {
	ID              int64           `json:"id"`
	OK              bool            `json:"ok"`
	Result          json.RawMessage `json:"result"`
	Error           string          `json:"error"`
	ApprovalRequest *approvalReq    `json:"approvalRequest"`
}

type approvalReq struct {
	App                     string `json:"app"`
	DisplayName             string `json:"displayName"`
	RiskLevel               string `json:"riskLevel"`
	AllowPersistentApproval *bool  `json:"allowPersistentApproval"`
}

func (a *approvalReq) toEngine() engine.ApprovalRequest {
	allow := true
	if a.AllowPersistentApproval != nil {
		allow = *a.AllowPersistentApproval
	}
	display := a.DisplayName
	if display == "" {
		display = a.App
	}
	return engine.ApprovalRequest{
		App:                     a.App,
		DisplayName:             display,
		RiskLevel:               a.RiskLevel,
		AllowPersistentApproval: allow,
	}
}

type pending struct {
	ch chan response
}

// proc 是一次 helper 子进程的生命周期。
//
// 每次（重新）启动都产生一个新的 proc；Client 只持有「当前那个」。
// 这样进程死后可以整体替换，而不是修补一个半死的对象。
type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	done   chan struct{} // 关闭表示进程已退出
	err    error         // 退出错误
}

// Client 是与 helper 子进程的会话。
//
// 并发安全：多个 MCP 请求可能同时到达。写 stdin 串行化（helper 是单进程），
// 读由单一 goroutine 负责并按 id 派发。
//
// 子进程会自动重启：用户按 Escape、引擎崩溃、被外部杀掉之后，
// 下一次调用会重新拉起，不需要重启整个 MCP server。
type Client struct {
	exe        string
	approver   engine.Approver
	writeMu    sync.Mutex // 串行化 stdin 写入
	mu         sync.Mutex // 保护以下字段
	cur        *proc      // 当前子进程；nil 表示未启动
	pendingMap map[int64]*pending
	nextID     atomic.Int64
	closing    bool // 用户主动关闭，不再重启
	// interrupted 为真表示用户按了 Escape。
	//
	// 这与「引擎崩溃」必须区别对待：崩溃要自动重启（恢复能力），
	// 而 Escape 是用户明确要求停下 —— 此时重启等于抹掉用户的意图。
	// 上游的做法是同一轮内直接拒绝后续请求，直到本轮结束。
	// 这里用同样语义：置位后拒绝一切调用，直到 TurnEnded 清除。
	interrupted bool
	startErr    error
	stderrBuf   *ringBuffer
}

// New 创建客户端（尚未启动子进程）。
func New(exe string, approver engine.Approver) *Client {
	return &Client{
		exe:        exe,
		approver:   approver,
		pendingMap: make(map[int64]*pending),
		stderrBuf:  newRingBuffer(8 << 10),
	}
}

// Start 拉起 helper 子进程。
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.startLocked()
	return err
}

// startLocked 启动一个子进程。调用方必须持有 c.mu。
func (c *Client) startLocked() (*proc, error) {
	// --parent-pid 让引擎知道父进程是谁；父进程消失时它会自行退出。
	cmd := exec.Command(c.exe, "--parent-pid", fmt.Sprint(pid()))
	setSysProcAttr(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stderr 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 helper 失败 (%s): %w", c.exe, err)
	}
	afterStart(cmd) // 平台相关：Windows 上挂 Job Object，确保父死子亡

	p := &proc{cmd: cmd, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	c.cur = p

	go c.readLoop(p)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				c.stderrBuf.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		werr := cmd.Wait()
		p.err = werr
		close(p.done)
		c.onProcExit(p)
	}()

	return p, nil
}

// onProcExit 处理子进程退出：唤醒所有等待者，并决定是否允许重启。
func (c *Client) onProcExit(p *proc) {
	err := procExitError(p.err)

	c.mu.Lock()
	// 只清理属于这个进程的 pending。
	pend := c.pendingMap
	c.pendingMap = make(map[int64]*pending)
	if c.cur == p {
		c.cur = nil
	}
	// 用户按 Escape → 本轮内不再自动重启。
	// 崩溃 / 被外部杀掉 → 允许重启，下次调用会透明恢复。
	if errors.Is(err, ErrInterrupted) {
		c.interrupted = true
	}
	c.mu.Unlock()

	for _, pd := range pend {
		select {
		case pd.ch <- response{Error: err.Error()}:
		default:
		}
	}
}

// procExitError 把退出码翻译成有意义的错误。
func procExitError(werr error) error {
	var ee *exec.ExitError
	if errors.As(werr, &ee) && ee.ExitCode() == exitInterrupted {
		return ErrInterrupted
	}
	if werr == nil {
		return errors.New("helper 已退出")
	}
	return fmt.Errorf("helper 已退出: %w", werr)
}

// readLoop 按行切分 stdout 并按 id 派发。
func (c *Client) readLoop(p *proc) {
	scanner := bufio.NewScanner(p.stdout)
	// 无障碍树和截图 base64 都可能很长，放大缓冲上限。
	scanner.Buffer(make([]byte, 64<<10), 64<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			// 无法解析的行不应打断整个会话，记下继续。
			continue
		}
		c.mu.Lock()
		pd, ok := c.pendingMap[resp.ID]
		if ok {
			delete(c.pendingMap, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			select {
			case pd.ch <- resp:
			default:
			}
		}
		// 没有对应 pending 的 id 直接丢弃（与上游一致）。
	}
}

// currentProc 返回当前子进程；若已退出则重新拉起。
//
// 「自动重启」只针对异常退出（崩溃、被外部杀掉）。
// 用户按 Escape 属于明确指令，不在此列 —— 见 interrupted 字段。
func (c *Client) currentProc() (*proc, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closing {
		return nil, errors.New("客户端已关闭")
	}
	if c.interrupted {
		return nil, ErrInterrupted
	}
	if c.cur != nil {
		select {
		case <-c.cur.done:
			// 已退出，走下面的重启逻辑。
		default:
			return c.cur, nil // 还活着，直接用
		}
	}
	return c.startLocked()
}

// Close 让引擎自己收尾，再结束子进程。
//
// 务必先走正常的 close 握手，不要一上来就 Kill。
// 引擎有个 cursor overlay —— 它会隐藏系统光标并自行绘制，
// 收到 close 才会把系统光标还原。强杀会把这个隐藏状态留在系统里，
// 表现为**整个桌面看不到鼠标指针**（实测踩过这个坑）。
//
// 因此：先发 close 请求，给它时间收尾；只有在超时或失败时才强杀。
func (c *Client) Close() error {
	c.mu.Lock()
	c.closing = true // 关闭后不再自动重启
	p := c.cur
	c.mu.Unlock()

	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}

	// 第一步：请求引擎收尾（还原光标、释放 UI Automation 钩子）。
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.dispatch(closeCtx, "close", map[string]any{}, nil, 4*time.Second); err == nil {
		select {
		case <-p.done:
			return nil
		case <-time.After(2 * time.Second):
			// 握手成功但进程没退，往下走强杀兜底。
		}
	}

	// 第二步：兜底强杀。
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.done
	return nil
}

// dispatch 发送一次请求并等待响应。
//
// 这是唯一的请求路径 —— 返回完整的 response，让调用方（approval.go）
// 能看到 approvalRequest，而不是把它塌缩成一个 error。
func (c *Client) dispatch(ctx context.Context, method string, params any, meta map[string]any, timeout time.Duration) (response, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	// 取当前子进程；已退出则自动重启。
	p, err := c.currentProc()
	if err != nil {
		return response{}, err
	}

	// meta 里带上预算，与上游行为一致。
	m := make(map[string]any, len(meta)+1)
	maps.Copy(m, meta)
	m[requestBudgetMetaKey] = timeout.Milliseconds()

	id := c.nextID.Add(1)
	payload := map[string]any{
		"id":     id,
		"method": method,
		"params": params,
		"meta":   m,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return response{}, fmt.Errorf("序列化请求失败: %w", err)
	}
	encoded = append(encoded, '\n')

	pd := &pending{ch: make(chan response, 1)}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return response{}, errors.New("客户端已关闭")
	}
	c.pendingMap[id] = pd
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pendingMap, id)
		c.mu.Unlock()
	}()

	c.writeMu.Lock()
	_, werr := p.stdin.Write(encoded)
	c.writeMu.Unlock()
	if werr != nil {
		return response{}, fmt.Errorf("写入 helper 失败: %w", werr)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-pd.ch:
		return resp, nil
	case <-timer.C:
		return response{}, fmt.Errorf("helper 请求超时 (%s, %s)", method, timeout)
	case <-ctx.Done():
		return response{}, ctx.Err()
	case <-p.done:
		return response{}, procExitError(p.err)
	}
}

// Stderr 返回 helper 最近的标准错误输出，供诊断。
func (c *Client) Stderr() string {
	return c.stderrBuf.String()
}
