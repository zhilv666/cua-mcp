// Package engine 定义 computer use 引擎的抽象接口。
//
// 抽这层是为了让 MCP 工具层不绑定具体后端。当前只有 codex 一个实现
// （包装 OpenAI 的 codex-computer-use.exe，仅 Windows）；日后若要跨平台，
// 可再加一个实现包装 open-computer-use，工具层无需改动。
package engine

import "context"

// Window 标识一个可操作的窗口。
//
// App 是引擎给出的应用标识：在 Windows 上通常是
// "process:C:\path\to\app.exe" 或 Win32 AppUserModelID；也可能是简短的
// "Chrome"。Id 是窗口句柄，仅在当前会话内有效。
type Window struct {
	App   string `json:"app" jsonschema:"应用标识，来自 list_windows 或 list_apps"`
	ID    int    `json:"id" jsonschema:"窗口 id，来自 list_windows"`
	Title string `json:"title,omitempty" jsonschema:"窗口标题（只读，回传时可省略）"`
}

// App 是 list_apps 返回的应用条目。
type App struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"displayName,omitempty"`
	Windows      []Window `json:"windows,omitempty"`
	LastUsedDate string   `json:"lastUsedDate,omitempty"`
	UseCount     int      `json:"useCount,omitempty"`
	IsRunning    bool     `json:"isRunning,omitempty"`
}

// Screenshot 是窗口截图。URL 是 data: URL（如 data:image/jpeg;base64,...）。
type Screenshot struct {
	ID      string `json:"id"`
	ZIndex  int    `json:"zIndex"`
	URL     string `json:"url"`
	OriginX int    `json:"originX,omitempty"`
	OriginY int    `json:"originY,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
}

// Accessibility 是窗口的无障碍树快照。
type Accessibility struct {
	Tree             string   `json:"tree"`
	FocusedElement   string   `json:"focused_element,omitempty"`
	SelectedText     string   `json:"selected_text,omitempty"`
	SelectedElements []string `json:"selected_elements,omitempty"`
	DocumentText     string   `json:"document_text,omitempty"`
}

// WindowState 是 get_window_state 的结果。
type WindowState struct {
	Window        Window         `json:"window"`
	Screenshots   []Screenshot   `json:"screenshots"`
	Accessibility *Accessibility `json:"accessibility"`
}

// Point 是屏幕坐标。
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// ClickTarget 描述点击目标：要么是按元素索引，要么是按坐标。
// 两者互斥 —— 引擎对这两条路径用的是不同的方法。
type ClickTarget struct {
	ElementIndex *int   `json:"element_index,omitempty"`
	Point        *Point `json:"point,omitempty"`
	ScreenshotID string `json:"screenshotId,omitempty"`
	MouseButton  string `json:"mouse_button,omitempty"`
	ClickCount   int    `json:"click_count,omitempty"`
}

// StateOptions 控制 get_window_state 取哪些内容。
type StateOptions struct {
	IncludeText       bool
	IncludeScreenshot bool
}

// Engine 是 computer use 引擎。
//
// 所有方法都应遵循 ctx 的取消与超时。实现需保证并发安全：
// 多个 MCP 请求可能同时到达。
type Engine interface {
	ListWindows(ctx context.Context) ([]Window, error)
	ListApps(ctx context.Context) ([]App, error)
	GetWindow(ctx context.Context, id int) (Window, error)
	GetWindowState(ctx context.Context, w Window, opts StateOptions) (WindowState, error)

	Click(ctx context.Context, w Window, target ClickTarget) error
	TypeText(ctx context.Context, w Window, text string) error
	PressKey(ctx context.Context, w Window, key string) error
	Scroll(ctx context.Context, w Window, at Point, deltaX, deltaY int) error
	Drag(ctx context.Context, w Window, from, to Point) error
	SetValue(ctx context.Context, w Window, elementIndex int, value string) error
	SecondaryAction(ctx context.Context, w Window, elementIndex int, action string) error

	LaunchApp(ctx context.Context, app string) error
	ActivateWindow(ctx context.Context, w Window) error

	// TurnEnded 通知引擎「本轮结束」，释放本轮的临时批准。
	// 宿主应在每轮对话结束时调用。
	TurnEnded(ctx context.Context) error

	Close() error
}

// Decision 是审批结果。
type Decision struct {
	Allow   bool
	Persist bool // 是否写入长期白名单
	Reason  string
}

// ApprovalRequest 是引擎报告的一次待审批操作。
type ApprovalRequest struct {
	App                     string `json:"app"`
	DisplayName             string `json:"displayName"`
	RiskLevel               string `json:"riskLevel,omitempty"`
	AllowPersistentApproval bool   `json:"allowPersistentApproval"`
}

// Approver 决定一次操作是否放行。
//
// 之所以抽象，是因为两种引擎的审批模型不同：Codex helper 会返回
// approvalRequest 让调用方决定；open-computer-use 则没有审批往返。
type Approver interface {
	Decide(ctx context.Context, req ApprovalRequest) (Decision, error)
}
