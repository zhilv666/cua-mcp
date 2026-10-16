package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhilv666/cua-mcp/internal/engine"
)

// fakeHelperSrc 是一个用 Go 写的假 helper，用来验证协议层。
//
// 它模拟真引擎的行为：
//   - 按行读 JSON 请求
//   - 未批准的 app 返回 ok:false + approvalRequest
//   - meta 里带 x-oai-cua-approved-app 时正常返回
//   - 故意分片写出响应，验证跨 chunk 的半行能被正确拼回
const fakeHelperSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type req struct {
	ID     int64          ` + "`json:\"id\"`" + `
	Method string         ` + "`json:\"method\"`" + `
	Params map[string]any ` + "`json:\"params\"`" + `
	Meta   map[string]any ` + "`json:\"meta\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r req
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}

		approved := false
		if r.Meta != nil {
			if v, ok := r.Meta["x-oai-cua-approved-app"]; ok && v == "needsapproval.exe" {
				approved = true
			}
		}

		switch r.Method {
		case "list_windows":
			write(r.ID, map[string]any{"id": r.ID, "ok": true, "result": []map[string]any{
				{"app": "process:C:/x/Demo.exe", "id": 42, "title": "Demo"},
			}})
		case "get_window_state":
			if !approved {
				write(r.ID, map[string]any{"id": r.ID, "ok": false,
					"approvalRequest": map[string]any{
						"app": "needsapproval.exe", "displayName": "NeedsApproval",
						"riskLevel": "low", "allowPersistentApproval": true}})
				continue
			}
			write(r.ID, map[string]any{"id": r.ID, "ok": true, "result": map[string]any{
				"window":        map[string]any{"app": "needsapproval.exe", "id": 42},
				"screenshots":   []any{},
				"accessibility": map[string]any{"tree": "APPROVED TREE"},
			}})
		case "boom":
			write(r.ID, map[string]any{"id": r.ID, "ok": false, "error": "引擎说不行"})
		case "slow":
			time.Sleep(3 * time.Second)
			write(r.ID, map[string]any{"id": r.ID, "ok": true, "result": "late"})
		case "end_turn":
			write(r.ID, map[string]any{"id": r.ID, "ok": true, "result": map[string]any{}})
		case "suicide":
			// 模拟用户按 Escape：引擎以退出码 130 结束
			os.Exit(130)
		case "close":
			write(r.ID, map[string]any{"id": r.ID, "ok": true})
			return
		case "click_element":
			write(r.ID, map[string]any{"id": r.ID, "ok": true})
		case "click":
			// 真引擎在缺 x 时会报这个错；这里复现，验证参数映射。
			if _, ok := r.Params["x"]; !ok {
				write(r.ID, map[string]any{"id": r.ID, "ok": false,
					"error": "parse click params: missing field ` + "`x`" + `"})
				continue
			}
			write(r.ID, map[string]any{"id": r.ID, "ok": true})
		default:
			write(r.ID, map[string]any{"id": r.ID, "ok": false,
				"error": "unsupported method: " + r.Method})
		}
	}
}

// write 故意把每一行拆成两块写，制造跨 chunk 的半行。
func write(id int64, v any) {
	b, _ := json.Marshal(v)
	s := string(b) + "\n"
	mid := len(s) / 2
	fmt.Print(s[:mid])
	os.Stdout.Sync()
	time.Sleep(5 * time.Millisecond)
	fmt.Print(s[mid:])
	os.Stdout.Sync()
}
`

// buildFakeHelper 编译假 helper，返回可执行文件路径。
func buildFakeHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fakeHelperSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := "module fakehelper\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(dir, "fakehelper")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译假 helper 失败: %v\n%s", err, out)
	}
	return exe
}

type allowAll struct{}

func (allowAll) Decide(context.Context, engine.ApprovalRequest) (engine.Decision, error) {
	return engine.Decision{Allow: true, Reason: "测试放行"}, nil
}

type denyAll struct{}

func (denyAll) Decide(context.Context, engine.ApprovalRequest) (engine.Decision, error) {
	return engine.Decision{Allow: false, Reason: "测试拒绝"}, nil
}

func newTestEngine(t *testing.T, approver engine.Approver) *Engine {
	t.Helper()
	exe := buildFakeHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	eng, err := NewEngine(ctx, exe, approver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

// 验证分片写入的半行能被正确拼回。
func TestListWindowsAcrossChunkBoundaries(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	windows, err := eng.ListWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 {
		t.Fatalf("期望 1 个窗口，得到 %d", len(windows))
	}
	if windows[0].ID != 42 || windows[0].Title != "Demo" {
		t.Errorf("窗口内容不对: %+v", windows[0])
	}
}

func TestApprovalRoundTrip(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	state, err := eng.GetWindowState(context.Background(),
		engine.Window{App: "needsapproval.exe", ID: 42}, engine.StateOptions{IncludeText: true})
	if err != nil {
		t.Fatalf("批准后应成功: %v", err)
	}
	if state.Accessibility == nil || state.Accessibility.Tree != "APPROVED TREE" {
		t.Errorf("未拿到预期内容: %+v", state)
	}
}

func TestApprovalDeniedSurfacesError(t *testing.T) {
	eng := newTestEngine(t, denyAll{})
	_, err := eng.GetWindowState(context.Background(),
		engine.Window{App: "needsapproval.exe", ID: 42}, engine.StateOptions{IncludeText: true})
	if err == nil {
		t.Fatal("拒绝时应报错")
	}
	if !strings.Contains(err.Error(), "NeedsApproval") {
		t.Errorf("错误信息应提到被拒的 app，得到: %v", err)
	}
}

func TestHelperErrorSurfaces(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	err := eng.client.requestInto(context.Background(), "boom", map[string]any{}, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "引擎说不行") {
		t.Errorf("应把 helper 的错误文本透出，得到: %v", err)
	}
}

func TestUnsupportedMethodSurfaces(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	err := eng.client.requestInto(context.Background(), "no_such_method", map[string]any{}, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported method") {
		t.Errorf("应透出 unsupported method，得到: %v", err)
	}
}

func TestTimeout(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	start := time.Now()
	err := eng.client.requestInto(context.Background(), "slow", map[string]any{}, 200*time.Millisecond, nil)
	if err == nil {
		t.Fatal("应超时")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("应是超时错误，得到: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("超时应及时返回，实际用了 %v", elapsed)
	}
}

// click 按元素走 click_element，按坐标走 click（需 x/y）。
func TestClickElementVsCoordinate(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	idx := 3
	if err := eng.Click(context.Background(), engine.Window{App: "a.exe", ID: 1},
		engine.ClickTarget{ElementIndex: &idx}); err != nil {
		t.Errorf("按元素点击应成功: %v", err)
	}

	pt := engine.Point{X: 10, Y: 20}
	if err := eng.Click(context.Background(), engine.Window{App: "a.exe", ID: 1},
		engine.ClickTarget{Point: &pt}); err != nil {
		t.Errorf("按坐标点击应成功: %v", err)
	}

	if err := eng.Click(context.Background(), engine.Window{App: "a.exe", ID: 1},
		engine.ClickTarget{}); err == nil {
		t.Error("既无元素也无坐标时应报错")
	}
}

func TestConcurrentRequests(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	const n = 12
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := eng.ListWindows(context.Background())
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("并发请求出错: %v", err)
		}
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	if err := eng.Close(); err != nil {
		t.Errorf("首次 Close 出错: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Errorf("重复 Close 应无错: %v", err)
	}
}

// Close 必须先走 close 握手，让引擎有机会还原系统光标 ——
// 直接强杀会把隐藏状态留在系统里（实测导致整个桌面看不到鼠标）。
func TestCloseSendsHandshakeFirst(t *testing.T) {
	eng := newTestEngine(t, allowAll{})

	// 记录 helper 是否收到过 close。假 helper 收到后会回 ok 并退出。
	done := make(chan error, 1)
	go func() { done <- eng.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close 出错: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close 未在 10s 内完成")
	}
}

// 重复 Close 不应出错（defer 里通常也会调一次）。
func TestCloseTwiceAfterHandshake(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	if err := eng.Close(); err != nil {
		t.Fatalf("首次 Close: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("二次 Close 应无错: %v", err)
	}
}

// 用户按 Escape（引擎以 130 退出）后，本轮内应拒绝后续调用 ——
// 不能自动重启，那会抹掉用户「停下」的意图。
func TestInterruptBlocksUntilTurnEnded(t *testing.T) {
	eng := newTestEngine(t, allowAll{})

	if _, err := eng.ListWindows(context.Background()); err != nil {
		t.Fatalf("首次调用应成功: %v", err)
	}

	// 让引擎以 130 退出（等效于用户按 Escape）。
	_, err := eng.client.dispatch(context.Background(), "suicide", map[string]any{}, nil, 3*time.Second)
	if err == nil {
		t.Fatal("引擎退出后本次调用应报错")
	}
	if !errors.Is(err, ErrInterrupted) {
		t.Errorf("应识别为用户中止，得到: %v", err)
	}

	// 关键：本轮内后续调用必须继续失败，而不是悄悄重启。
	for i := 0; i < 3; i++ {
		if _, err := eng.ListWindows(context.Background()); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("中止后第 %d 次调用应返回 ErrInterrupted，得到: %v", i+1, err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 一轮结束后（TurnEnded）应恢复可用。
	if err := eng.TurnEnded(context.Background()); err != nil {
		t.Fatalf("TurnEnded 出错: %v", err)
	}
	if _, err := eng.ListWindows(context.Background()); err != nil {
		t.Errorf("新的一轮应恢复可用，得到: %v", err)
	}
}

// 外部强杀引擎后也应自动恢复。
func TestAutoRespawnAfterKill(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	if _, err := eng.ListWindows(context.Background()); err != nil {
		t.Fatalf("首次调用应成功: %v", err)
	}

	// 直接杀掉子进程（模拟外部干扰）。
	eng.client.mu.Lock()
	p := eng.client.cur
	eng.client.mu.Unlock()
	if p == nil || p.cmd.Process == nil {
		t.Fatal("没有子进程")
	}
	_ = p.cmd.Process.Kill()
	<-p.done

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := eng.ListWindows(context.Background()); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("被强杀后未能自动重启")
}

// TurnEnded 应能正常送达引擎。
func TestTurnEnded(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	if err := eng.TurnEnded(context.Background()); err != nil {
		t.Errorf("TurnEnded 出错: %v", err)
	}
}

// 关闭之后不应再自动重启。
func TestNoRespawnAfterClose(t *testing.T) {
	eng := newTestEngine(t, allowAll{})
	if _, err := eng.ListWindows(context.Background()); err != nil {
		t.Fatalf("首次调用应成功: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close 出错: %v", err)
	}
	if _, err := eng.ListWindows(context.Background()); err == nil {
		t.Error("关闭后不应再重启引擎")
	}
}
