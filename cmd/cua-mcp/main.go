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
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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
			return runAllowlistCmd(os.Args[1], os.Args[2:])
		case "cursor":
			return runCursorCmd(os.Args[2:])
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
	)
	fs.Usage = usage
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *showVer {
		fmt.Println("cua-mcp 1.0.0")
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

	allowlistPath := *listPath
	if allowlistPath == "" {
		allowlistPath = sky.DefaultAllowlistPath()
	}

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

func runAllowlistCmd(cmd string, args []string) error {
	path := sky.DefaultAllowlistPath()
	if p := os.Getenv("CUA_ALLOWLIST"); p != "" {
		path = p
	}

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

选项：
  --auto              跳过审批询问，全部放行（有风险）
  --helper <path>     指定 codex-computer-use.exe
  --allowlist <path>  指定审批白名单文件
  --version           显示版本

环境变量：
  CUA_HELPER_PATH     直接指定 helper exe
  CUA_NODE_RUNTIME    指定 cua_node 运行时目录
  CODEX_HOME          影响白名单的默认位置
`)
}
