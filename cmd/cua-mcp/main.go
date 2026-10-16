// Command cua-mcp 是一个独立的 Computer Use MCP 服务器。
//
// 它包装 OpenAI Codex 随附的 codex-computer-use.exe（Windows 原生应用自动化），
// 把能力暴露成 MCP 工具，支持 stdio / Streamable HTTP / SSE 三种传输，
// 可脱离 Codex App 运行。
//
// 用法：
//
//	cua-mcp                        # stdio（默认）
//	cua-mcp --http :8765           # Streamable HTTP
//	cua-mcp --sse :8766            # HTTP+SSE（旧协议）
//	cua-mcp --http :8765 --sse :8766   # 同时开
//	cua-mcp --auto                 # 跳过审批询问，全部放行
//	cua-mcp approvals              # 查看白名单
//	cua-mcp approve <app>          # 加入白名单
//	cua-mcp revoke <app>           # 移出白名单
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhilv666/cua-mcp/internal/cursor"
	"github.com/zhilv666/cua-mcp/internal/engine/codex"
	"github.com/zhilv666/cua-mcp/internal/mcpserver"
	"github.com/zhilv666/cua-mcp/internal/sky"
)

// 构建信息，由 -ldflags "-X main.xxx=..." 注入。
//
// 全部留空时回落到运行时能拿到的信息（go install 场景，
// 或直接 go build 出来的二进制）—— 这样「--version 永远是同一个值」的
// 假象不会出现。
var (
	version   = "" // 版本号，如 v1.2.3
	commit    = "" // 短 commit hash
	buildTime = "" // 构建时间，RFC3339
	buildOS   = "" // 目标平台
	buildArch = "" // 目标架构
)

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// version 各字段的取值，缺失的字段回落到运行时信息或省略。
type versionInfo struct {
	Name    string // 程序名
	Version string // 版本号
	Commit  string // 短 commit
	OS      string // 目标平台
	Arch    string // 目标架构
	Time    string // 构建时间
}

func collectVersion() versionInfo {
	v := versionInfo{
		Name:    "cua-mcp",
		Version: buildVersion(),
		Commit:  commit,
		OS:      buildOS,
		Arch:    buildArch,
		Time:    buildTime,
	}
	// 平台/架构：没注入就用当前运行时的
	if v.OS == "" {
		v.OS = runtime.GOOS
	}
	if v.Arch == "" {
		v.Arch = runtime.GOARCH
	}
	// commit：没注入时从构建元信息里取。
	if v.Commit == "" {
		v.Commit = vcsRevision()
	}
	return v
}

// versionText 生成人类可读的多行输出：字段名 + 值，每行一个。
//
// 原先挤在一行，值一多就难对位。分行的好处是报 bug 时
// 直接整段贴出来就行，谁也不用数第几个字段是哪个。
func versionText(v versionInfo) string {
	const labelWidth = 10 // 按显示列宽对齐，CJK 算两列
	rows := [][2]string{
		{"版本", v.Version},
		{"提交", v.Commit},
		{"平台", v.OS},
		{"架构", v.Arch},
		{"构建时间", v.Time},
	}
	var b strings.Builder
	for _, r := range rows {
		if r[1] == "" {
			continue // 取不到的字段不占行
		}
		pad := max(labelWidth-displayWidth(r[0]), 1)
		b.WriteString(r[0])
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(r[1])
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// displayWidth 估算字符串在终端里的显示列数。
//
// Go 的 fmt 按「字符数」补位，而中文一个字占两列 ——
// 直接用 %-8s 会让 "版本" 和 "构建时间" 两行对不齐。
// 这里把常见 CJK 区段按两列计。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && r <= 0x115F, // 朝鲜文字母
			r >= 0x2E80 && r <= 0xA4CF, // CJK 部首、假名、汉字等
			r >= 0xAC00 && r <= 0xD7A3, // 谚文音节
			r >= 0xF900 && r <= 0xFAFF, // CJK 兼容汉字
			r >= 0xFE30 && r <= 0xFE6F, // CJK 兼容形式
			r >= 0xFF00 && r <= 0xFF60, // 全角形式
			r >= 0xFFE0 && r <= 0xFFE6:
			w += 2
		default:
			w++
		}
	}
	return w
}

// versionJSON 生成单行 JSON，便于脚本消费。
func versionJSON(v versionInfo) (string, error) {
	out, err := json.Marshal(map[string]string{
		"name":    v.Name,
		"version": v.Version,
		"commit":  v.Commit,
		"os":      v.OS,
		"arch":    v.Arch,
		"time":    v.Time,
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// vcsRevision 从构建元信息里取 commit（go build 默认会写进去）。
func vcsRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return ""
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 子命令：白名单管理与光标修复（都不需要拉起引擎）
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "approvals", "approve", "revoke":
			// 子命令也接受 --allowlist，与服务器走同一套解析。
			path, rest, err := parseAllowlistFlag(os.Args[2:])
			if err != nil {
				return err
			}
			return runAllowlistCmd(os.Args[1], rest, path)
		case "cursor":
			return runCursorCmd(os.Args[2:])
		case "doctor", "where":
			return runDoctorCmd()
		case "help":
			usage()
			return nil
		}
	}

	fs := flag.NewFlagSet("cua-mcp", flag.ContinueOnError)
	var (
		httpAddr   = fs.String("http", "", "Streamable HTTP 监听地址，如 :8765")
		sseAddr    = fs.String("sse", "", "HTTP+SSE 监听地址，如 :8766")
		auto       = fs.Bool("auto", false, "跳过审批询问，全部放行（有风险）")
		helperPath = fs.String("helper", "", "codex-computer-use.exe 路径（默认自动查找）")
		listPath   = fs.String("allowlist", "", "审批白名单路径（默认用 Codex 的那份）")
		showVer    = fs.Bool("version", false, "显示版本")
		verJSON    = fs.Bool("json", false, "以 JSON 输出（配合 --version）")
	)
	// -v 作为 --version 的短选项。flag 包对同一变量注册两个名字即可。
	fs.BoolVar(showVer, "v", false, "显示版本（--version 的简写）")
	fs.Usage = usage

	if err := fs.Parse(os.Args[1:]); err != nil {
		// flag 遇到 -h / --help 会打印用法后返回 ErrHelp。
		// 那是正常的求助行为，不是错误 —— 直接返回，别再往上抛，
		// 否则用户会看到「错误：flag: help requested」这种莫名其妙的提示。
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVer {
		info := collectVersion()
		if *verJSON {
			s, err := versionJSON(info)
			if err != nil {
				return err
			}
			fmt.Println(s)
			return nil
		}
		fmt.Println(versionText(info))
		return nil
	}

	// 定位引擎。
	exe := *helperPath
	if exe == "" {
		h, err := sky.Find()
		if err != nil {
			return err
		}
		exe = h.Exe
	}

	allowlistPath := sky.ResolveAllowlistPath(*listPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	approver := mcpserver.NewApprover(allowlistPath, *auto, logf)

	// stdio 下不能往 stdout 写任何非协议内容，诊断一律走 stderr。
	logf("引擎：%s", exe)
	logf("白名单：%s", allowlistPath)
	if *auto {
		logf("审批：auto（全部放行）")
	} else {
		logf("审批：ask（按白名单，首次询问）")
	}

	eng, err := codex.NewEngine(ctx, exe, approver)
	if err != nil {
		return err
	}
	defer eng.Close()

	srv := mcpserver.New(eng)

	// 至少要有一种传输。
	mode := *httpAddr != "" || *sseAddr != ""
	if !mode {
		// stdio
		logf("传输：stdio")
		err := srv.MCP().Run(ctx, &mcp.StdioTransport{})
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}

	return runHTTP(ctx, srv, *httpAddr, *sseAddr)
}

// runHTTP 在同一进程里同时提供 HTTP 与 SSE。
//
// 两种传输共用同一个 *mcp.Server，工具只注册一次；
// getServer 闭包对每个请求都返回同一个实例。
func runHTTP(ctx context.Context, srv *mcpserver.Server, httpAddr, sseAddr string) error {
	mux := http.NewServeMux()
	getServer := func(*http.Request) *mcp.Server { return srv.MCP() }

	var servers []*http.Server
	var addrs []string

	if httpAddr != "" {
		h := mcp.NewStreamableHTTPHandler(getServer, nil)
		mux.Handle("/mcp", h)
		mux.Handle("/mcp/", h)
		addrs = append(addrs, "http://"+normalizeAddr(httpAddr)+"/mcp")
	}
	if sseAddr != "" {
		h := mcp.NewSSEHandler(getServer, nil)
		mux.Handle("/sse", h)
		mux.Handle("/sse/", h)
		addrs = append(addrs, "http://"+normalizeAddr(sseAddr)+"/sse")
	}

	// 健康检查，方便确认服务活着。
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// 两个监听地址分别起服务；同一 mux 复用一个 handler。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	listen := func(addr string) {
		s := &http.Server{Addr: addr, Handler: mux}
		servers = append(servers, s)
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logf("监听 %s 失败：%v", addr, err)
			}
		}()
	}
	// 同一个 mux 绑两个端口时，只需起一个监听即可；
	// 但 HTTP 与 SSE 若给了不同端口，则各自监听。
	if httpAddr != "" && sseAddr != "" && httpAddr != sseAddr {
		listen(httpAddr)
		listen(sseAddr)
	} else if httpAddr != "" {
		listen(httpAddr)
	} else {
		listen(sseAddr)
	}

	for _, a := range addrs {
		logf("传输：%s", a)
	}
	logf("就绪。Ctrl+C 退出。")

	<-ctx.Done()
	logf("正在关闭…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	return nil
}

func normalizeAddr(a string) string {
	if strings.HasPrefix(a, ":") {
		return "127.0.0.1" + a
	}
	return a
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cua] "+format+"\n", args...)
}

// parseAllowlistFlag 从子命令参数里摘出 --allowlist。
//
// 子命令不用 flag 包（位置参数更自然），所以这里手写：
// 识别 --allowlist <path> 与 --allowlist=<path>，其余原样返回。
func parseAllowlistFlag(args []string) (path string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--allowlist":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--allowlist 后面需要一个路径")
			}
			path = args[i+1]
			i++
		case strings.HasPrefix(a, "--allowlist="):
			path = strings.TrimPrefix(a, "--allowlist=")
		default:
			rest = append(rest, a)
		}
	}
	return path, rest, nil
}

func runAllowlistCmd(cmd string, args []string, explicitPath string) error {
	path := sky.ResolveAllowlistPath(explicitPath)

	switch cmd {
	case "approvals":
		apps := sky.ReadAllowlist(path)
		fmt.Printf("白名单文件：%s\n", path)
		if len(apps) == 0 {
			fmt.Println("长期允许：（空）")
			return nil
		}
		sort.Strings(apps)
		fmt.Printf("长期允许（%d）：\n", len(apps))
		for _, a := range apps {
			fmt.Printf("  %s\n", a)
		}
		return nil

	case "approve", "revoke":
		if len(args) == 0 {
			return fmt.Errorf("%s 需要一个 app 标识", cmd)
		}
		app := args[0]
		var err error
		if cmd == "approve" {
			err = sky.Grant(path, app)
			if err == nil {
				fmt.Printf("已允许：%s\n", app)
			}
		} else {
			err = sky.Revoke(path, app)
			if err == nil {
				fmt.Printf("已移除：%s\n", app)
			}
		}
		return err
	}
	return fmt.Errorf("未知命令：%s", cmd)
}

// runDoctorCmd 打印引擎查找结果，便于排查「找不到引擎」。
//
// 自动查找涉及多个安装位置，出问题时只看一句「找不到」无从下手。
// 这个子命令把每个候选位置都列出来。
func runDoctorCmd() error {
	fmt.Println("cua-mcp " + buildVersion())
	fmt.Printf("平台：%s/%s\n\n", runtime.GOOS, runtime.GOARCH)

	fmt.Println("引擎查找：")
	for _, c := range sky.Candidates() {
		mark := "  "
		if c.Found {
			mark = "✅"
		}
		fmt.Printf("  %s %s\n", mark, c.Path)
		if c.Found {
			fmt.Printf("       └─ %s\n", c.Exe)
		}
	}

	h, err := sky.Find()
	fmt.Println()
	if err != nil {
		fmt.Println("结果：❌ 未找到引擎")
		fmt.Println()
		fmt.Println(err.Error())
		fmt.Println("\n可用 CUA_HELPER_PATH 直接指定该文件的路径。")
		return nil
	}
	fmt.Println("结果：✅ 已找到")
	fmt.Printf("  引擎：%s\n", h.Exe)
	fmt.Printf("  树根：%s\n", h.Root)
	fmt.Printf("  白名单：%s\n", sky.ResolveAllowlistPath(""))
	return nil
}

// runCursorCmd 处理 `cua-mcp cursor ...`。
//
// 引擎的 cursor overlay 会接管系统光标。正常退出会还原，
// 但被强杀时可能来不及 —— 此时桌面上的鼠标指针会消失。
// 这个子命令用来检查与修复。
func runCursorCmd(args []string) error {
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}

	before := cursor.Probe()
	fmt.Printf("光标状态：%s\n", before)

	switch action {
	case "status", "check":
		if before.LooksHijacked() {
			fmt.Println("→ 异常：光标可能被接管（位图为空或不可见）")
			fmt.Println("  执行 `cua-mcp cursor fix` 修复")
			return nil
		}
		fmt.Println("→ 正常")
		return nil

	case "fix", "restore":
		if !before.LooksHijacked() {
			fmt.Println("→ 看起来正常，无需修复。")
			fmt.Println("  若屏幕上仍看不到光标，可加 --force 强制重装。")
			if len(args) > 1 && (args[1] == "--force" || args[1] == "-f") {
				// 继续往下走
			} else {
				return nil
			}
		}
		n, err := cursor.Restore()
		if err != nil {
			return err
		}
		after := cursor.Probe()
		fmt.Printf("→ 已重装 %d 项系统光标，现在：%s\n", n, after)
		return nil

	default:
		return fmt.Errorf("未知的光标命令：%s（可用：status / fix）", action)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `独立 Computer Use MCP 服务器

用法：
  cua-mcp                            以 stdio 运行（默认）
  cua-mcp --http :8765               以 Streamable HTTP 运行
  cua-mcp --sse  :8766               以 HTTP+SSE 运行
  cua-mcp --http :8765 --sse :8766   两者同时

  cua-mcp approvals                  查看审批白名单
  cua-mcp approve <app>              加入白名单
  cua-mcp revoke <app>               移出白名单

  cua-mcp cursor status              检查系统光标是否正常
  cua-mcp cursor fix                 重装系统光标（光标不见了时用）

  cua-mcp doctor                     显示引擎查找结果（找不到引擎时先跑这个）

选项：
  --auto              跳过审批询问，全部放行（有风险）
  --helper <path>     指定 codex-computer-use.exe
  --allowlist <path>  指定审批白名单文件（服务器与子命令通用）
  -v, --version       显示版本（含提交、平台/架构、构建时间）
  --json              配合 --version 以 JSON 输出
  -h, --help          显示本帮助

环境变量：
  CUA_HELPER_PATH     直接指定 helper exe
  CUA_NODE_RUNTIME    指定 cua_node 运行时目录
  CUA_ALLOWLIST       覆盖白名单文件位置（低于 --allowlist）
  CODEX_HOME          影响白名单的默认位置

白名单优先级：--allowlist > CUA_ALLOWLIST > $CODEX_HOME/computer-use/config.toml
路径支持 ~ 开头。
`)
}
