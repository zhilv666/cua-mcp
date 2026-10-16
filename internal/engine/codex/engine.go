package codex

import (
	"context"
	"errors"

	"github.com/zhilv666/cua-mcp/internal/engine"
)

// errNoClickTarget 表示既没给元素索引也没给坐标。
var errNoClickTarget = errors.New("click 需要 element_index 或 point 之一")

// Engine 把 Client 适配成 engine.Engine。
type Engine struct {
	client *Client
}

// NewEngine 启动 helper 并返回可用的引擎。
func NewEngine(ctx context.Context, exe string, approver engine.Approver) (*Engine, error) {
	c := New(exe, approver)
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	return &Engine{client: c}, nil
}

var _ engine.Engine = (*Engine)(nil)

// windowParam 把 Window 序列化成引擎要的形状。
//
// title 是只读信息，回传时不必再发；只发 app 和 id。
func windowParam(w engine.Window) map[string]any {
	p := map[string]any{"app": w.App, "id": w.ID}
	if w.Title != "" {
		p["title"] = w.Title
	}
	return p
}

func (e *Engine) ListWindows(ctx context.Context) ([]engine.Window, error) {
	var out []engine.Window
	err := e.client.requestInto(ctx, "list_windows", map[string]any{}, 0, &out)
	return out, err
}

func (e *Engine) ListApps(ctx context.Context) ([]engine.App, error) {
	var out []engine.App
	err := e.client.requestInto(ctx, "list_apps", map[string]any{}, 0, &out)
	return out, err
}

func (e *Engine) GetWindow(ctx context.Context, id int) (engine.Window, error) {
	var out engine.Window
	err := e.client.requestInto(ctx, "get_window", map[string]any{"id": id}, 0, &out)
	return out, err
}

func (e *Engine) GetWindowState(ctx context.Context, w engine.Window, opts engine.StateOptions) (engine.WindowState, error) {
	var out engine.WindowState
	err := e.client.requestInto(ctx, "get_window_state", map[string]any{
		"window":             windowParam(w),
		"include_text":       opts.IncludeText,
		"include_screenshot": opts.IncludeScreenshot,
	}, 0, &out)
	return out, err
}

// Click 点击一个元素或一个坐标。
//
// 关键：这两条路径走的是**不同的方法**。按元素走 click_element，
// 按坐标走 click —— 后者要求 x/y 存在，否则引擎报
// "parse click params: missing field `x`"。
func (e *Engine) Click(ctx context.Context, w engine.Window, target engine.ClickTarget) error {
	if target.ElementIndex != nil {
		params := map[string]any{
			"window":        windowParam(w),
			"element_index": *target.ElementIndex,
		}
		if target.ClickCount > 0 {
			params["click_count"] = target.ClickCount
		}
		if target.MouseButton != "" {
			params["mouse_button"] = target.MouseButton
		}
		return e.client.requestInto(ctx, "click_element", params, 0, nil)
	}

	if target.Point == nil {
		return errNoClickTarget
	}
	params := map[string]any{
		"window": windowParam(w),
		"x":      target.Point.X,
		"y":      target.Point.Y,
	}
	if target.ScreenshotID != "" {
		params["screenshotId"] = target.ScreenshotID
	}
	if target.ClickCount > 0 {
		params["click_count"] = target.ClickCount
	}
	if target.MouseButton != "" {
		params["mouse_button"] = target.MouseButton
	}
	return e.client.requestInto(ctx, "click", params, 0, nil)
}

func (e *Engine) TypeText(ctx context.Context, w engine.Window, text string) error {
	return e.client.requestInto(ctx, "type_text", map[string]any{
		"window": windowParam(w),
		"text":   text,
	}, 0, nil)
}

func (e *Engine) PressKey(ctx context.Context, w engine.Window, key string) error {
	return e.client.requestInto(ctx, "press_key", map[string]any{
		"window": windowParam(w),
		"key":    key,
	}, 0, nil)
}

func (e *Engine) Scroll(ctx context.Context, w engine.Window, at engine.Point, deltaX, deltaY int) error {
	params := map[string]any{
		"window":  windowParam(w),
		"x":       at.X,
		"y":       at.Y,
		"scrollX": deltaX,
		"scrollY": deltaY,
	}
	return e.client.requestInto(ctx, "scroll", params, 0, nil)
}

func (e *Engine) Drag(ctx context.Context, w engine.Window, from, to engine.Point) error {
	return e.client.requestInto(ctx, "drag", map[string]any{
		"window": windowParam(w),
		"from_x": from.X,
		"from_y": from.Y,
		"to_x":   to.X,
		"to_y":   to.Y,
	}, 0, nil)
}

func (e *Engine) SetValue(ctx context.Context, w engine.Window, elementIndex int, value string) error {
	return e.client.requestInto(ctx, "set_value", map[string]any{
		"window":        windowParam(w),
		"element_index": elementIndex,
		"value":         value,
	}, 0, nil)
}

func (e *Engine) SecondaryAction(ctx context.Context, w engine.Window, elementIndex int, action string) error {
	return e.client.requestInto(ctx, "perform_secondary_action", map[string]any{
		"window":        windowParam(w),
		"element_index": elementIndex,
		"action":        action,
	}, 0, nil)
}

func (e *Engine) LaunchApp(ctx context.Context, app string) error {
	// 拉起应用较慢，用更长的预算。
	return e.client.requestInto(ctx, "launch_app", map[string]any{"app": app}, launchAppTimeout, nil)
}

func (e *Engine) ActivateWindow(ctx context.Context, w engine.Window) error {
	return e.client.requestInto(ctx, "activate_window", map[string]any{
		"window": windowParam(w),
	}, 0, nil)
}

func (e *Engine) Close() error {
	return e.client.Close()
}

// TurnEnded 通知引擎「本轮结束」，释放本轮的应用批准。
//
// 上游用 MCP hook 在 Interrupt / Stop / SubagentStop 时调用它。
// 不调用的话，批准过的 app 会一直保持批准状态，不再重新征询。
//
// 它同时是「Escape 中止」的解除点：用户按 ESC 后本轮不再受理请求，
// 直到调用 TurnEnded 开启新的一轮。这与上游的 turn 作用域语义一致。
func (e *Engine) TurnEnded(ctx context.Context) error {
	// 先解除 Escape 中止状态，并允许引擎重启。
	e.client.mu.Lock()
	wasInterrupted := e.client.interrupted
	e.client.interrupted = false
	e.client.mu.Unlock()

	if !wasInterrupted {
		// 没被中止过，正常通知引擎即可。
		_, err := e.client.dispatch(ctx, "end_turn", map[string]any{}, nil, defaultTimeout)
		return err
	}

	// 被 Escape 中止过：引擎可能已经退出，dispatch 会自动重启它。
	// 新进程本来就是干净的一轮，无需再发 end_turn。
	return nil
}

// Stderr 返回 helper 最近的标准错误输出，供诊断。
func (e *Engine) Stderr() string {
	return e.client.Stderr()
}
