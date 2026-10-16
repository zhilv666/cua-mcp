// Package mcpserver 把 engine.Engine 暴露成一组 MCP 工具。
//
// 这一层是 Engine 接口的唯一消费者 —— 换后端（如日后的 open-computer-use）
// 不需要改动这里。
package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhilv666/cua-mcp/internal/engine"
)

// Server 持有引擎并注册工具。
type Server struct {
	engine engine.Engine
	mcp    *mcp.Server
}

// New 创建一个 MCP server，并注册全部 computer use 工具。
//
// 同一个 *mcp.Server 可以同时喂给 stdio / HTTP / SSE 三种传输，
// 工具只注册一次。
func New(eng engine.Engine) *Server {
	s := &Server{
		engine: eng,
		mcp: mcp.NewServer(&mcp.Implementation{
			Name:    "computer-use",
			Version: "1.0.0",
		}, &mcp.ServerOptions{
			Instructions: instructions,
		}),
	}
	s.register()
	return s
}

// MCP 返回底层的 *mcp.Server，供各传输复用。
func (s *Server) MCP() *mcp.Server { return s.mcp }

const instructions = `通过无障碍树读取和操作本机原生应用的界面。

先用 list_windows 拿到窗口，再用 get_window_state 看界面内容。
界面里的每个元素前面都有编号，点击/输入时用 element_index 引用它。

首选的顺序：能用专门的 API、CLI 或技能完成的，优先用那些；
只有必须操作图形界面时才用本工具。`

// ---------- 输入类型 ----------

// 注意：这些结构体的 json tag 同时用作 MCP 的输入 schema，
// jsonschema tag 提供字段说明给模型看。

type listWindowsIn struct{}

type listAppsIn struct{}

type getWindowIn struct {
	ID int `json:"id" jsonschema:"窗口 id，来自 list_windows"`
}

type getWindowStateIn struct {
	Window            engine.Window `json:"window" jsonschema:"目标窗口，用 list_windows 拿到的 {app,id}"`
	IncludeText       *bool         `json:"include_text,omitempty" jsonschema:"是否附带无障碍树文本，默认 true"`
	IncludeScreenshot *bool         `json:"include_screenshot,omitempty" jsonschema:"是否附带截图，默认 false"`
}

type clickIn struct {
	Window       engine.Window `json:"window" jsonschema:"目标窗口"`
	ElementIndex *int          `json:"element_index,omitempty" jsonschema:"元素编号（与 x/y 二选一）"`
	X            *int          `json:"x,omitempty" jsonschema:"坐标 x（与 element_index 二选一）"`
	Y            *int          `json:"y,omitempty" jsonschema:"坐标 y（与 element_index 二选一）"`
	MouseButton  string        `json:"mouse_button,omitempty" jsonschema:"left/right/middle，默认 left"`
	// 可选数值一律用指针：显式传 null 时不该在 schema 校验阶段炸掉。
	ClickCount   *int   `json:"click_count,omitempty" jsonschema:"点击次数，默认 1"`
	ScreenshotID string `json:"screenshot_id,omitempty" jsonschema:"基于哪张截图的坐标"`
}

type typeTextIn struct {
	Window engine.Window `json:"window" jsonschema:"目标窗口"`
	Text   string        `json:"text" jsonschema:"要输入的文本"`
}

type pressKeyIn struct {
	Window engine.Window `json:"window" jsonschema:"目标窗口"`
	Key    string        `json:"key" jsonschema:"按键或组合键，用 + 连接，如 Control_L+c"`
}

type scrollIn struct {
	Window engine.Window `json:"window" jsonschema:"目标窗口"`
	X      int           `json:"x" jsonschema:"滚动位置 x"`
	Y      int           `json:"y" jsonschema:"滚动位置 y"`
	DeltaX int           `json:"delta_x,omitempty" jsonschema:"水平滚动量，右为正"`
	DeltaY int           `json:"delta_y,omitempty" jsonschema:"垂直滚动量，下为正"`
}

type dragIn struct {
	Window engine.Window `json:"window" jsonschema:"目标窗口"`
	FromX  int           `json:"from_x" jsonschema:"起点 x"`
	FromY  int           `json:"from_y" jsonschema:"起点 y"`
	ToX    int           `json:"to_x" jsonschema:"终点 x"`
	ToY    int           `json:"to_y" jsonschema:"终点 y"`
}

// 注意：element_index 用指针而非 int。
// MCP 客户端可能把可选字段显式传成 null，用 int 会在 schema 校验阶段
// 报 "type: <invalid reflect.Value> has type null, want integer" ——
// 那是校验器的内部措辞，模型看了也不知道错在哪。
// 用指针可以让 null 落到我们自己的入参检查里，给出可读的提示。
type setValueIn struct {
	Window       engine.Window `json:"window" jsonschema:"目标窗口"`
	ElementIndex *int          `json:"element_index" jsonschema:"元素编号，来自 get_window_state"`
	Value        string        `json:"value" jsonschema:"要设置的值"`
}

type secondaryActionIn struct {
	Window       engine.Window `json:"window" jsonschema:"目标窗口"`
	ElementIndex *int          `json:"element_index" jsonschema:"元素编号，来自 get_window_state"`
	Action       string        `json:"action" jsonschema:"次要动作名，如 Raise、Scroll Up、Expand、Collapse"`
}

type launchAppIn struct {
	App string `json:"app" jsonschema:"应用名或可执行文件路径"`
}

type activateWindowIn struct {
	Window engine.Window `json:"window" jsonschema:"要激活的窗口"`
}

type turnEndedIn struct{}

// ---------- 输出类型 ----------

type listWindowsOut struct {
	Windows []engine.Window `json:"windows"`
	Count   int             `json:"count"`
}

type listAppsOut struct {
	Apps  []engine.App `json:"apps"`
	Count int          `json:"count"`
}

type getWindowStateOut struct {
	Window       engine.Window `json:"window"`
	Text         string        `json:"text,omitempty"`
	Focused      string        `json:"focused_element,omitempty"`
	DocumentText string        `json:"document_text,omitempty"`
	HasShot      bool          `json:"has_screenshot"`
	ShotCount    int           `json:"screenshot_count"`
}

type okOut struct {
	OK bool `json:"ok"`
}

// ---------- 注册 ----------

func (s *Server) register() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_windows",
		Description: "列出当前有窗口的应用。返回每个窗口的 {app,id,title}，后续操作用这些值引用它。",
	}, s.handleListWindows)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_apps",
		Description: "列出所有已安装/已知的应用（含未运行的）。比 list_windows 更全，用于查找想启动的应用。",
	}, s.handleListApps)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_window",
		Description: "按 id 取单个窗口的信息。",
	}, s.handleGetWindow)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_window_state",
		Description: "读取窗口当前界面。返回无障碍树文本（每个元素带编号，供 click/set_value 引用）" +
			"以及可选的截图。操作界面后应先调用它看看结果。",
	}, s.handleGetWindowState)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "click",
		Description: "点击窗口里的元素或某个坐标。优先用 element_index（更稳），" +
			"只有在无从获得元素编号时才用 x/y。",
	}, s.handleClick)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "type_text",
		Description: "向窗口输入文本。",
	}, s.handleTypeText)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "press_key",
		Description: "按键或组合键，用 + 连接，如 Return、Tab、Control_L+c、Control_L+Shift_L+period。",
	}, s.handlePressKey)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "scroll",
		Description: "在窗口的某个位置滚动。delta_y 为正向下，delta_x 为正向右。",
	}, s.handleScroll)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "drag",
		Description: "从一点拖拽到另一点。",
	}, s.handleDrag)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "set_value",
		Description: "直接设置某个元素的值（比逐字输入快，适合输入框）。",
	}, s.handleSetValue)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "perform_secondary_action",
		Description: "对元素执行次要动作，如 Raise、Scroll Up、Expand、Collapse。",
	}, s.handleSecondaryAction)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "launch_app",
		Description: "启动一个应用。应用名来自 list_apps。",
	}, s.handleLaunchApp)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "activate_window",
		Description: "把窗口带到前台。",
	}, s.handleActivateWindow)

	// turn_ended 是给宿主的清理钩子：一轮对话结束时调用，
	// 让引擎释放本轮临时批准的应用。模型一般不该主动调它。
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "turn_ended",
		Description: "通知 Computer Use 引擎本轮已结束，释放本轮临时批准的应用。" +
			"宿主在每轮对话结束时调用；模型通常不需要主动调用。",
	}, s.handleTurnEnded)
}

// ---------- 处理函数 ----------

func textResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

func (s *Server) handleListWindows(ctx context.Context, _ *mcp.CallToolRequest, _ listWindowsIn) (*mcp.CallToolResult, listWindowsOut, error) {
	windows, err := s.engine.ListWindows(ctx)
	if err != nil {
		return nil, listWindowsOut{}, err
	}
	if len(windows) == 0 {
		return textResult("当前没有可操作的窗口。"), listWindowsOut{Windows: []engine.Window{}}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "找到 %d 个窗口：\n", len(windows))
	for _, w := range windows {
		name := shortApp(w.App)
		if w.Title != "" {
			fmt.Fprintf(&b, "  [%d] %s — %s\n", w.ID, name, w.Title)
		} else {
			fmt.Fprintf(&b, "  [%d] %s\n", w.ID, name)
		}
	}
	res := listWindowsOut{Windows: windows, Count: len(windows)}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: b.String()}},
		StructuredContent: res,
	}, res, nil
}

func (s *Server) handleListApps(ctx context.Context, _ *mcp.CallToolRequest, _ listAppsIn) (*mcp.CallToolResult, listAppsOut, error) {
	apps, err := s.engine.ListApps(ctx)
	if err != nil {
		return nil, listAppsOut{}, err
	}
	var b strings.Builder
	running, total := 0, len(apps)
	for _, a := range apps {
		if a.IsRunning {
			running++
		}
	}
	fmt.Fprintf(&b, "共 %d 个应用（%d 个在运行）：\n", total, running)
	for _, a := range apps {
		mark := "  "
		if a.IsRunning {
			mark = "* "
		}
		name := a.DisplayName
		if name == "" {
			name = shortApp(a.ID)
		}
		fmt.Fprintf(&b, "%s%s", mark, name)
		if len(a.Windows) > 0 {
			fmt.Fprintf(&b, "（%d 个窗口）", len(a.Windows))
		}
		b.WriteString("\n")
	}
	res := listAppsOut{Apps: apps, Count: total}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: b.String()}},
		StructuredContent: res,
	}, res, nil
}

func (s *Server) handleGetWindow(ctx context.Context, _ *mcp.CallToolRequest, in getWindowIn) (*mcp.CallToolResult, engine.Window, error) {
	w, err := s.engine.GetWindow(ctx, in.ID)
	if err != nil {
		return nil, engine.Window{}, err
	}
	return textResult("窗口 %d：%s（%s）", w.ID, w.Title, shortApp(w.App)), w, nil
}

func (s *Server) handleGetWindowState(ctx context.Context, _ *mcp.CallToolRequest, in getWindowStateIn) (*mcp.CallToolResult, getWindowStateOut, error) {
	// 状态查询本身要耗时数秒，给宽一点的预算由引擎处理。
	includeText := true // 默认要文本：没有它，模型看不到界面
	includeShot := false
	if in.IncludeText != nil {
		includeText = *in.IncludeText
	}
	if in.IncludeScreenshot != nil {
		includeShot = *in.IncludeScreenshot
	}
	if !includeText && !includeShot {
		includeText = true // 两者都不给就没意义了
	}

	state, err := s.engine.GetWindowState(ctx, in.Window, engine.StateOptions{
		IncludeText:       includeText,
		IncludeScreenshot: includeShot,
	})
	if err != nil {
		return nil, getWindowStateOut{}, err
	}

	out := getWindowStateOut{
		Window:    state.Window,
		HasShot:   len(state.Screenshots) > 0,
		ShotCount: len(state.Screenshots),
	}
	var content []mcp.Content

	if state.Accessibility != nil {
		out.Text = state.Accessibility.Tree
		out.Focused = state.Accessibility.FocusedElement
		out.DocumentText = state.Accessibility.DocumentText
		if includeText && out.Text != "" {
			text := out.Text
			if out.Focused != "" {
				text += "\n\n" + out.Focused
			}
			content = append(content, &mcp.TextContent{Text: text})
		}
	}
	if len(content) == 0 {
		content = append(content, &mcp.TextContent{Text: "（没有可读的界面内容）"})
	}

	// 截图作为 image content 回传。
	if includeShot {
		for _, shot := range state.Screenshots {
			img, err := decodeDataURL(shot.URL)
			if err != nil {
				continue
			}
			content = append(content, &mcp.ImageContent{
				Data:     img.data,
				MIMEType: img.mime,
			})
		}
	}

	return &mcp.CallToolResult{Content: content, StructuredContent: out}, out, nil
}

func (s *Server) handleClick(ctx context.Context, _ *mcp.CallToolRequest, in clickIn) (*mcp.CallToolResult, okOut, error) {
	target := engine.ClickTarget{
		ElementIndex: in.ElementIndex,
		MouseButton:  in.MouseButton,
		ScreenshotID: in.ScreenshotID,
	}
	if in.ClickCount != nil {
		target.ClickCount = *in.ClickCount
	}
	if in.ElementIndex == nil {
		if in.X == nil || in.Y == nil {
			return nil, okOut{}, fmt.Errorf("需要 element_index，或同时提供 x 与 y")
		}
		target.Point = &engine.Point{X: *in.X, Y: *in.Y}
	}
	if err := s.engine.Click(ctx, in.Window, target); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已点击。"), okOut{OK: true}, nil
}

func (s *Server) handleTypeText(ctx context.Context, _ *mcp.CallToolRequest, in typeTextIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.TypeText(ctx, in.Window, in.Text); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已输入 %d 个字符。", len([]rune(in.Text))), okOut{OK: true}, nil
}

func (s *Server) handlePressKey(ctx context.Context, _ *mcp.CallToolRequest, in pressKeyIn) (*mcp.CallToolResult, okOut, error) {
	if strings.TrimSpace(in.Key) == "" {
		return nil, okOut{}, fmt.Errorf("key 不能为空")
	}
	if err := s.engine.PressKey(ctx, in.Window, in.Key); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已按下 %s。", in.Key), okOut{OK: true}, nil
}

func (s *Server) handleScroll(ctx context.Context, _ *mcp.CallToolRequest, in scrollIn) (*mcp.CallToolResult, okOut, error) {
	if in.DeltaX == 0 && in.DeltaY == 0 {
		return nil, okOut{}, fmt.Errorf("delta_x 与 delta_y 不能同时为 0")
	}
	if err := s.engine.Scroll(ctx, in.Window, engine.Point{X: in.X, Y: in.Y}, in.DeltaX, in.DeltaY); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已滚动（%d, %d）。", in.DeltaX, in.DeltaY), okOut{OK: true}, nil
}

func (s *Server) handleDrag(ctx context.Context, _ *mcp.CallToolRequest, in dragIn) (*mcp.CallToolResult, okOut, error) {
	err := s.engine.Drag(ctx, in.Window,
		engine.Point{X: in.FromX, Y: in.FromY},
		engine.Point{X: in.ToX, Y: in.ToY})
	if err != nil {
		return nil, okOut{}, err
	}
	return textResult("已从 (%d,%d) 拖到 (%d,%d)。", in.FromX, in.FromY, in.ToX, in.ToY), okOut{OK: true}, nil
}

func (s *Server) handleSetValue(ctx context.Context, _ *mcp.CallToolRequest, in setValueIn) (*mcp.CallToolResult, okOut, error) {
	if in.ElementIndex == nil {
		return nil, okOut{}, fmt.Errorf("需要 element_index（元素编号来自 get_window_state）")
	}
	if err := s.engine.SetValue(ctx, in.Window, *in.ElementIndex, in.Value); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已设置元素 %d 的值。", *in.ElementIndex), okOut{OK: true}, nil
}

func (s *Server) handleSecondaryAction(ctx context.Context, _ *mcp.CallToolRequest, in secondaryActionIn) (*mcp.CallToolResult, okOut, error) {
	if in.ElementIndex == nil {
		return nil, okOut{}, fmt.Errorf("需要 element_index（元素编号来自 get_window_state）")
	}
	if strings.TrimSpace(in.Action) == "" {
		return nil, okOut{}, fmt.Errorf("action 不能为空")
	}
	if err := s.engine.SecondaryAction(ctx, in.Window, *in.ElementIndex, in.Action); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已执行 %s。", in.Action), okOut{OK: true}, nil
}

func (s *Server) handleLaunchApp(ctx context.Context, _ *mcp.CallToolRequest, in launchAppIn) (*mcp.CallToolResult, okOut, error) {
	if strings.TrimSpace(in.App) == "" {
		return nil, okOut{}, fmt.Errorf("app 不能为空")
	}
	if err := s.engine.LaunchApp(ctx, in.App); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已启动 %s。", in.App), okOut{OK: true}, nil
}

func (s *Server) handleActivateWindow(ctx context.Context, _ *mcp.CallToolRequest, in activateWindowIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.ActivateWindow(ctx, in.Window); err != nil {
		return nil, okOut{}, err
	}
	return textResult("已激活窗口 %d。", in.Window.ID), okOut{OK: true}, nil
}

func (s *Server) handleTurnEnded(ctx context.Context, _ *mcp.CallToolRequest, _ turnEndedIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.TurnEnded(ctx); err != nil {
		return nil, okOut{}, err
	}
	return textResult("本轮已结束，临时批准已释放。"), okOut{OK: true}, nil
}

// ---------- 辅助 ----------

// shortApp 把长路径形式的 app 标识缩短成可读名字。
// "process:C:\Program Files\Zed\Zed.exe" → "Zed.exe"
func shortApp(app string) string {
	s := strings.TrimPrefix(app, "process:")
	if i := strings.LastIndexAny(s, `\/`); i >= 0 {
		return s[i+1:]
	}
	return s
}

type decodedImage struct {
	data []byte
	mime string
}

// decodeDataURL 解析 data:image/jpeg;base64,... 形式的截图。
func decodeDataURL(u string) (decodedImage, error) {
	const prefix = "data:"
	if !strings.HasPrefix(u, prefix) {
		return decodedImage{}, fmt.Errorf("不是 data URL")
	}
	header, payload, ok := strings.Cut(u[len(prefix):], ",")
	if !ok {
		return decodedImage{}, fmt.Errorf("data URL 缺少逗号")
	}

	mime := "image/jpeg"
	if m, _, ok := strings.Cut(header, ";"); ok {
		mime = m
	} else {
		mime = header
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return decodedImage{}, err
	}
	return decodedImage{data: data, mime: mime}, nil
}
