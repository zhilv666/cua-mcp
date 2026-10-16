package mcpserver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/zhilv666/cua-mcp/internal/engine"
	"github.com/zhilv666/cua-mcp/internal/sky"
)

// Approver 按「app 白名单 + 首次询问」决定是否放行。
//
// 白名单与 Codex 自己那份共用同一个文件（~/.codex/computer-use/config.toml），
// 所以 App 里批准过的 app 这里也认，反之亦然。
type Approver struct {
	Path string
	// Auto 为 true 时全部放行（等价于 --auto）。
	Auto bool
	// Prompt 负责与用户交互。为 nil 时用控制台交互。
	// 无终端可用时应返回 ErrNoConsole，此时按拒绝处理。
	Prompt func(ctx context.Context, msg string) (answer string, err error)
	// Log 输出诊断。
	Log func(format string, args ...any)

	mu      sync.Mutex
	session map[string]bool // 本次进程内已放行的 app
}

// NewApprover 创建审批器。
func NewApprover(path string, auto bool, log func(string, ...any)) *Approver {
	return &Approver{Path: path, Auto: auto, Log: log, session: map[string]bool{}}
}

// ErrNoConsole 表示拿不到交互终端，无法征询用户。
var ErrNoConsole = fmt.Errorf("没有可用的交互终端")

var _ engine.Approver = (*Approver)(nil)

func (a *Approver) log(format string, args ...any) {
	if a.Log != nil {
		a.Log(format, args...)
	}
}

// Decide 实现 engine.Approver。
func (a *Approver) Decide(ctx context.Context, req engine.ApprovalRequest) (engine.Decision, error) {
	app := req.App
	display := req.DisplayName
	if display == "" {
		display = app
	}

	if a.Auto {
		return a.settle(app, engine.Decision{Allow: true, Persist: true, Reason: "auto 模式"}), nil
	}

	// 已在长期白名单。
	if sky.ContainsApp(sky.ReadAllowlist(a.Path), app) {
		return engine.Decision{Allow: true, Persist: true, Reason: "已在白名单"}, nil
	}

	a.mu.Lock()
	approvedThisSession := a.session[app]
	a.mu.Unlock()
	if approvedThisSession {
		return engine.Decision{Allow: true, Persist: false, Reason: "本次会话已批准"}, nil
	}

	allowPersist := req.AllowPersistentApproval
	var hint string
	if allowPersist {
		hint = "  [y] 仅本次   [a] 始终允许   [n] 拒绝  "
	} else {
		hint = "  [y] 允许   [n] 拒绝  "
	}
	prompt := fmt.Sprintf("允许 Computer Use 操作「%s」(%s) 吗？\n%s", display, app, hint)

	raw, err := a.ask(ctx, prompt)
	if err != nil {
		// 拿不到终端 → 保守拒绝。绝不能默默放行。
		a.log("审批：无法询问（%v），按拒绝处理 app=%s", err, app)
		return engine.Decision{Allow: false, Reason: "无法询问（无终端）"}, nil
	}

	answer := strings.ToLower(strings.TrimSpace(raw))
	switch answer {
	case "a", "always":
		if allowPersist {
			return a.settle(app, engine.Decision{Allow: true, Persist: true, Reason: "用户选择始终允许"}), nil
		}
		return a.settle(app, engine.Decision{Allow: true, Reason: "用户允许（引擎不允许持久）"}), nil
	case "y", "yes":
		return a.settle(app, engine.Decision{Allow: true, Reason: "用户仅本次允许"}), nil
	default:
		return engine.Decision{Allow: false, Reason: "用户拒绝"}, nil
	}
}

// settle 记录放行结果。
//
// 放行必须落到状态里，否则下次还要重问 —— Persist 为真时写长期白名单
// （与 Codex 共用那份），否则只记在本次会话。
func (a *Approver) settle(app string, d engine.Decision) engine.Decision {
	if !d.Allow {
		return d
	}
	a.mu.Lock()
	a.session[app] = true
	a.mu.Unlock()

	if d.Persist {
		if err := sky.Grant(a.Path, app); err != nil {
			// 写白名单失败不影响本次放行，但要留痕并降级为会话级。
			a.log("审批：写白名单失败（%v），本次仍放行但不持久化 app=%s", err, app)
			d.Persist = false
		}
	}
	return d
}

// Grant 把 app 加入长期白名单并记入本次会话。
func (a *Approver) Grant(app string) error {
	if app == "" {
		return fmt.Errorf("app 不能为空")
	}
	return sky.Grant(a.Path, app)
}

// Revoke 从长期白名单移除。
func (a *Approver) Revoke(app string) error {
	return sky.Revoke(a.Path, app)
}

func (a *Approver) ask(ctx context.Context, msg string) (string, error) {
	if a.Prompt != nil {
		return a.Prompt(ctx, msg)
	}
	return consolePrompt(ctx, msg)
}

// consolePrompt 直接打开控制台设备读取确认。
//
// MCP server 的 stdin 是 JSON-RPC 通道，绝不能用来读确认，
// 所以这里另开 CONIN$/CONOUT$。取不到就返回 ErrNoConsole，
// 由调用方按拒绝处理。
func consolePrompt(ctx context.Context, msg string) (string, error) {
	in, err := openConsole("CONIN$")
	if err != nil {
		return "", ErrNoConsole
	}
	defer in.Close()
	out, err := openConsoleOut("CONOUT$")
	if err != nil {
		return "", ErrNoConsole
	}
	defer out.Close()

	if _, err := fmt.Fprint(out, msg); err != nil {
		return "", ErrNoConsole
	}

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(in)
		line, err := reader.ReadString('\n')
		ch <- result{line: line, err: err}
	}()

	select {
	case r := <-ch:
		if r.err != nil && r.line == "" && r.err != io.EOF {
			return "", r.err
		}
		fmt.Fprintln(out)
		return strings.TrimSpace(r.line), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
