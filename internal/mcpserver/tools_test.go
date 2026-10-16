package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhilv666/cua-mcp/internal/engine"
)

// stubEngine 记录调用，用于验证工具层的入参处理。
type stubEngine struct {
	lastSetValue struct {
		idx   int
		value string
	}
	lastClick engine.ClickTarget
	lastAct   string
}

func (s *stubEngine) ListWindows(context.Context) ([]engine.Window, error) {
	return []engine.Window{{App: "a.exe", ID: 1, Title: "A"}}, nil
}
func (s *stubEngine) ListApps(context.Context) ([]engine.App, error) {
	return []engine.App{{ID: "a.exe", DisplayName: "A"}}, nil
}
func (s *stubEngine) GetWindow(context.Context, int) (engine.Window, error) {
	return engine.Window{App: "a.exe", ID: 1}, nil
}
func (s *stubEngine) GetWindowState(context.Context, engine.Window, engine.StateOptions) (engine.WindowState, error) {
	return engine.WindowState{Accessibility: &engine.Accessibility{Tree: "TREE"}}, nil
}
func (s *stubEngine) Click(_ context.Context, _ engine.Window, t engine.ClickTarget) error {
	s.lastClick = t
	return nil
}
func (s *stubEngine) TypeText(context.Context, engine.Window, string) error { return nil }
func (s *stubEngine) PressKey(context.Context, engine.Window, string) error { return nil }
func (s *stubEngine) Scroll(context.Context, engine.Window, engine.Point, int, int) error {
	return nil
}
func (s *stubEngine) Drag(context.Context, engine.Window, engine.Point, engine.Point) error {
	return nil
}
func (s *stubEngine) SetValue(_ context.Context, _ engine.Window, idx int, v string) error {
	s.lastSetValue.idx = idx
	s.lastSetValue.value = v
	return nil
}
func (s *stubEngine) SecondaryAction(_ context.Context, _ engine.Window, idx int, a string) error {
	s.lastSetValue.idx = idx
	s.lastAct = a
	return nil
}
func (s *stubEngine) LaunchApp(context.Context, string) error             { return nil }
func (s *stubEngine) ActivateWindow(context.Context, engine.Window) error { return nil }
func (s *stubEngine) TurnEnded(context.Context) error                     { return nil }
func (s *stubEngine) Close() error                                        { return nil }

// callTool 通过真实 MCP 服务器调用一个工具，返回原始的 CallToolResult。
//
// 走 InMemoryTransport 而不是直接调 handler —— 这样 JSON schema 校验
// 也会真实发生，正是我们要验的部分。
func callTool(t *testing.T, srv *Server, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()

	c, s := mcp.NewInMemoryTransports()
	serverSession, err := srv.MCP().Connect(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: json.RawMessage(raw),
	})
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", name, err)
	}
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// 显式传 null 的可选字段不应在 schema 校验阶段炸掉 ——
// 这是实测踩到的：模型把可选字段填成 null 时，
// 校验器会吐 "type: <invalid reflect.Value> has type null, want integer"。
func TestNullOptionalFieldsAreReadable(t *testing.T) {
	eng := &stubEngine{}
	srv := New(eng)
	win := map[string]any{"app": "a.exe", "id": 1}

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string // 期望错误里出现的关键词
	}{
		{"set_value element_index=null", "set_value",
			map[string]any{"window": win, "element_index": nil, "value": "x"}, "element_index"},
		{"secondary_action element_index=null", "perform_secondary_action",
			map[string]any{"window": win, "element_index": nil, "action": "Raise"}, "element_index"},
		{"click element_index=null 且无坐标", "click",
			map[string]any{"window": win, "element_index": nil}, "element_index"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, srv, tc.tool, tc.args)
			msg := textOf(res)
			if strings.Contains(msg, "invalid reflect.Value") ||
				strings.Contains(msg, "cannot unmarshal") {
				t.Fatalf("校验崩溃了，应给出可读错误: %s", msg)
			}
			if !res.IsError {
				t.Fatalf("应报错，得到: %s", msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("错误信息应提到 %q，得到: %s", tc.want, msg)
			}
		})
	}
}

// click_count 传 null 应视为「用默认值」，照常执行。
func TestClickCountNullUsesDefault(t *testing.T) {
	eng := &stubEngine{}
	srv := New(eng)

	res := callTool(t, srv, "click", map[string]any{
		"window":        map[string]any{"app": "a.exe", "id": 1},
		"element_index": 7,
		"click_count":   nil,
	})
	if res.IsError {
		t.Fatalf("click_count=null 应正常执行，得到: %s", textOf(res))
	}
	if eng.lastClick.ElementIndex == nil || *eng.lastClick.ElementIndex != 7 {
		t.Errorf("element_index 应传成 7，得到 %+v", eng.lastClick.ElementIndex)
	}
	if eng.lastClick.ClickCount != 0 {
		t.Errorf("click_count 未给时应为 0（由引擎取默认），得到 %d", eng.lastClick.ClickCount)
	}
}

// get_window_state 的布尔可选字段传 null 应生效默认值。
func TestGetWindowStateNullBooleans(t *testing.T) {
	eng := &stubEngine{}
	srv := New(eng)

	res := callTool(t, srv, "get_window_state", map[string]any{
		"window":             map[string]any{"app": "a.exe", "id": 1},
		"include_text":       nil,
		"include_screenshot": nil,
	})
	if res.IsError {
		t.Fatalf("应为 null 容错，得到: %s", textOf(res))
	}
	if !strings.Contains(textOf(res), "TREE") {
		t.Errorf("默认应返回文本，得到: %s", textOf(res))
	}
}

// 正常路径不该被这些改动破坏。
func TestNormalCallsStillWork(t *testing.T) {
	eng := &stubEngine{}
	srv := New(eng)

	if res := callTool(t, srv, "set_value", map[string]any{
		"window": map[string]any{"app": "a.exe", "id": 1}, "element_index": 3, "value": "hi",
	}); res.IsError {
		t.Fatalf("set_value 应成功: %s", textOf(res))
	}
	if eng.lastSetValue.idx != 3 || eng.lastSetValue.value != "hi" {
		t.Errorf("set_value 参数不对: %+v", eng.lastSetValue)
	}

	if res := callTool(t, srv, "click", map[string]any{
		"window": map[string]any{"app": "a.exe", "id": 1}, "x": 10, "y": 20, "click_count": 2,
	}); res.IsError {
		t.Fatalf("按坐标点击应成功: %s", textOf(res))
	}
	if eng.lastClick.Point == nil || eng.lastClick.Point.X != 10 || eng.lastClick.ClickCount != 2 {
		t.Errorf("坐标点击参数不对: %+v", eng.lastClick)
	}
}

// 工具必须全部注册成功。
func TestAllToolsRegistered(t *testing.T) {
	srv := New(&stubEngine{})
	ctx := context.Background()
	c, s := mcp.NewInMemoryTransports()
	ss, err := srv.MCP().Connect(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	cs, err := cl.Connect(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"list_windows", "list_apps", "get_window", "get_window_state",
		"click", "type_text", "press_key", "scroll", "drag", "set_value",
		"perform_secondary_action", "launch_app", "activate_window", "turn_ended"}
	got := map[string]bool{}
	for _, tl := range tools.Tools {
		got[tl.Name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("缺少工具: %s", w)
		}
	}
}
