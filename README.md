# cua-mcp

独立的 **Computer Use MCP 服务器** —— 把 Codex App 的电脑操作能力抽出来，
不需要一直开着 App。

Go 写的单文件二进制，支持 **stdio / Streamable HTTP / SSE** 三种传输，
可交叉编译到 Windows / macOS / Linux。

```bash
go build -o bin/cua-mcp.exe ./cmd/cua-mcp
```

---

## 一、背景：Codex App 其实只是个启动器

Codex App 并不自己实现电脑操作。它只做两件事：

```
Codex App (ChatGPT.exe)
   ├─ 启动 → %LOCALAPPDATA%\OpenAI\Codex\runtimes\cua_node\<hash>\
   │            bin\node_repl.exe                  NodeREPL（Rust 的 MCP stdio server）
   │            bin\node_modules\@oai\sky          桌面自动化引擎
   │            bin\node_modules\@oai\cua          cua API
   │            bin\node_modules\@oai\cua-repl     把上面包成 MCP server
   └─ 应答 → 审批弹窗「允许使用 <App>？」
```

本项目替换掉 App 的位置：**直接驱动引擎**，自己接管审批。
比原版更进一步 —— 连 Node 都不需要。

### 引擎可以直接驱动（已验证）

引擎是 `codex-computer-use.exe`，复制到任意位置都能独立运行，无需 Node：

```
→ {"id":1,"method":"list_windows","params":{}}
← {"id":1,"ok":true,"result":[{"app":"process:C:\\...\\Zed.exe","id":263908,"title":"cu"}...]}
```

---

## 二、⚠️ 平台事实：引擎只有 Windows

这点必须说清楚，不要被「跨平台」误导：

| | 本机是否存在 |
|---|---|
| `@oai/sky/bin/windows/codex-computer-use.exe` | ✅ 有 |
| `@oai/sky/bin/linux/sky_linux_x64` | ❌ 仅在 package.json 里声明 |
| `@oai/sky/bin/mac/.../Codex Computer Use.app` | ❌ 12 个路径全是声明 |

- **OpenAI 的 Sky 引擎是闭源的**（`plugin.json` 标 `"license": "Proprietary"`）。
- mac 那条路还要求 `nodeRepl.nativePipe` / `launchServices`（受信任的 Codex NodeREPL 宿主），
  外部进程够不到。
- 三个平台的**传输协议互不相同**：Windows 换行 `{id,method,params,meta}`；
  Linux 换行 `{id,command,input}`；macOS 是 4 字节小端长度前缀 JSON-RPC 2.0 走 UNIX socket。

**结论：包装层能跨平台，引擎不能。** 本二进制在 mac/Linux 上能编译也能跑，
但没有可驱动的引擎。

### 想要真跨平台？

社区有 MIT 的替代实现，都是（或将是）可插拔后端：

| 项目 | 语言 | 平台 | 传输 |
|---|---|---|---|
| [open-codex-computer-use](https://github.com/iFurySt/open-codex-computer-use) | Swift | 三平台，有预编译包 | 仅 stdio |
| [trycua/cua](https://github.com/trycua/cua) | Rust | 三平台 | stdio 完整，HTTP 为 legacy |

代码已按此预留：`internal/engine/engine.go` 定义 `Engine` 接口，
`internal/engine/codex/` 是当前实现。加后端只需再写一个实现，
`internal/mcpserver/` 不用改。

---

## 三、用法

### stdio（MCP 客户端）

```bash
cua-mcp
```

接入 Claude Code：

```bash
claude mcp add cua-mcp --scope user -- "<仓库路径>\bin\cua-mcp.exe"
```

> 名字不能叫 `computer-use`，那是 Claude Code 的保留名。

接入 Codex CLI（`~/.codex/config.toml`）：

```toml
[mcp_servers.cua_mcp]
command = '<仓库路径>\bin\cua-mcp.exe'
```

### HTTP / SSE

```bash
cua-mcp --http :8765                 # Streamable HTTP → http://127.0.0.1:8765/mcp
cua-mcp --sse  :8766                 # HTTP+SSE       → http://127.0.0.1:8766/sse
cua-mcp --http :8765 --sse :8766     # 同时开
```

两种传输共用同一个 server 实例，工具只注册一次。

### 命令行

```bash
cua-mcp approvals            # 查看白名单
cua-mcp approve <app>        # 加入白名单
cua-mcp revoke <app>         # 移出白名单
cua-mcp --version
```

### 选项与环境变量

| 选项 / 变量 | 说明 |
|---|---|
| `--auto` | 跳过审批询问，全部放行（有风险） |
| `--helper <path>` / `CUA_HELPER_PATH` | 指定 `codex-computer-use.exe` |
| `--allowlist <path>` / `CUA_ALLOWLIST` | 指定白名单文件 |
| `CUA_NODE_RUNTIME` | 指定 `cua_node` 运行时目录 |
| `CODEX_HOME` | 影响白名单默认位置 |

---

## 四、工具（14 个）

| 工具 | 说明 |
|---|---|
| `list_windows` | 列出有窗口的应用 |
| `list_apps` | 列出所有已知应用（含未运行） |
| `get_window` | 按 id 取窗口 |
| `get_window_state` | **读界面**：无障碍树（元素带编号）+ 可选截图 |
| `click` | 点元素（`element_index`）或坐标（`x`/`y`） |
| `type_text` | 输入文本 |
| `press_key` | 按键，`+` 连接组合键 |
| `scroll` / `drag` | 滚动 / 拖拽 |
| `set_value` | 直接设值（比逐字输入快） |
| `perform_secondary_action` | 次要动作：Raise / Expand / Collapse … |
| `launch_app` / `activate_window` | 启动应用 / 激活窗口 |
| `turn_ended` | 通知引擎本轮结束，释放本轮临时批准（宿主调用） |

典型流程：`list_windows` → `get_window_state` → `click(element_index)` → 再看状态。

---

## 五、Escape 中止与自动恢复

引擎自己监听**物理 Escape 键**（闭源二进制里的键盘钩子），用户按一下就能叫停。

按下后引擎以**退出码 130** 结束。这里有两种退出，必须区别对待：

| 情形 | 行为 |
|---|---|
| 用户按 **Escape** | **本轮内拒绝后续调用**，返回明确的「已被用户中止」提示 |
| 引擎**崩溃** / 被外部杀掉 | **自动重启**，下次调用透明恢复 |

Escape 不自动重启是有意的 —— 用户按 ESC 就是要求停下，
重启等于把用户的意图抹掉。这与上游的 turn 作用域语义一致：
要开始新的一轮，宿主调用 `turn_ended`，中止状态随之解除。

宿主应在每轮对话结束时调 `turn_ended`（对应上游的
`Interrupt` / `Stop` / `SubagentStop` hook），否则批准过的 app 会一直保持批准。

---

## 六、审批白名单

白名单**不另建一份**，直接读写 Codex 自己那份：

```
~/.codex/computer-use/config.toml
[apps]
allowed = ["zed.exe"]
```

这个文件是引擎自己维护的 —— 找到它才真正接管了审批：
**App 批准过的 app 这里认，这里批准过的 App 也认。**

策略：

- 已在白名单 → 静默通过
- 未知 app → 控制台询问 `[y] 仅本次 / [a] 始终允许 / [n] 拒绝`
- **拿不到终端 → 一律拒绝**（fail closed）
- `--auto` → 全部放行。省事，但代理被调用时可能直接操作你的任意窗口

> 作为 MCP server 被客户端拉起时，stdin 是 JSON-RPC 通道，
> 因此自然落入「拿不到终端 → 拒绝」分支。要无人值守地放行某些 app，
> 先 `cua-mcp approve <app>` 预置白名单。

---

## 七、代码结构

```
cmd/cua-mcp/main.go              入口：flag 选传输
internal/engine/engine.go        Engine + Approver 接口（换后端的边界）
internal/engine/codex/client.go  子进程 + NDJSON 收发 + id 关联 + 超时
internal/engine/codex/engine.go  Engine 的 codex 实现
internal/engine/codex/approval.go 审批往返（approvalRequest → 带 meta 重试）
internal/engine/codex/proc_windows.go  Job Object，父死子亡
internal/sky/locate.go           定位 helper exe
internal/sky/allowlist.go        读写 computer-use/config.toml
internal/cursor/                 光标接管检测与修复（Windows）
internal/mcpserver/tools.go      14 个工具
internal/mcpserver/approver.go   审批策略
```

### 逆向出来的协议要点

| 项 | 值 |
|---|---|
| 启动 | `<exe> --parent-pid <pid>`，stdio |
| 帧格式 | 换行分隔 JSON（**不是** Content-Length） |
| 请求 | `{id, method, params, meta}`（`meta` 里带 `x-oai-cua-request-budget-ms`） |
| 响应 | `{id, ok, result?, error?, approvalRequest?}` |
| 审批 | `ok:false` + `approvalRequest` → 询问 → 带 `meta["x-oai-cua-approved-app"]` 重试 |
| 超时 | 默认 10s，`launch_app` 15s |
| **坑** | 按元素点击的方法是 **`click_element`**，不是 `click`。`click` 是坐标分支，缺 x/y 会报 `missing field 'x'` |
| 响应耗时 | 可达 ~10s，不可当卡死 |
| **坑** | 可选数值字段必须用指针类型。客户端把可选字段显式传成 `null` 时，用 `int` 会在 schema 校验阶段报 `type: <invalid reflect.Value> has type null, want integer` —— 措辞是校验器的内部细节，模型看不懂。改成 `*int` 后 `null` 会落到自己的入参检查里，给出可读提示 |
| **坑** | 引擎带 **cursor overlay**：它会隐藏系统光标、自己绘制一个。正常退出会还原，强杀可能来不及 → 桌面看不到鼠标指针（详见下节） |

### ⚠️ 关于光标：为什么不能「拦截强杀」

引擎的 cursor overlay 会接管系统光标（隐藏 + 自绘）。若引擎被强杀且来不及清理，
**整个桌面的鼠标指针会消失**（鼠标仍可移动可点击，只是看不见）。

**Windows 没有可捕获的终止信号。** `TerminateProcess`（`proc.kill()` / `taskkill /F`
走的都是它）是瞬时的，目标进程一行代码都跑不了 —— 没法在进程内拦截强杀。
能捕获的只有 Ctrl+C、控制台关闭、stdin EOF。

**所以正确做法不是加拦截，而是别去杀它。** 实测结论：

| 做法 | 结果 |
|---|---|
| 父进程正常退出 / 被 `taskkill /F` 强杀 | ✅ helper 靠 `--parent-pid` **自己发现并优雅退出**（500ms 内），光标完好 |
| 用 Job Object `KILL_ON_JOB_CLOSE` | ❌ 系统**抢先强杀** helper，它来不及清理 → **光标丢失** |

一开始我加了 Job Object 防孤儿进程 —— 那是基于猜测，实测证明**多余且有害**：
helper 本来就会自己退，Job Object 只是掐掉了它的清理机会。现已移除。

```bash
cua-mcp cursor status        # 检查光标是否正常（看位图尺寸，不看标志位）
cua-mcp cursor fix           # 重装系统光标
cua-mcp cursor fix --force   # 状态看似正常但仍看不到时强制重装
```

> 判断依据是**光标位图尺寸**，不是 `GetCursorInfo` 的可见标志 ——
> 实测引擎把位图换成空白后该标志仍为 `true`。0x0 或 1x1 才算异常。

修复走注册表 API（`LoadImage` + `CopyImage` + `SetSystemCursor`），不依赖 PowerShell。
注意 `SetSystemCursor` 会**销毁**传入句柄，必须传 `CopyImage` 的副本，
否则会损坏共享的系统光标资源。`SPI_SETCURSORS` 单独调用不够 ——
光标被替换过时它不生效。

---

## 八、测试

```bash
go test ./...     # 64 项
go vet ./...
```

覆盖：白名单 TOML 往返（含保留其他段、多行数组、转义）、
NDJSON **跨 chunk 分片**拼帧、按 id 关联、并发、超时、
审批往返（放行 / 拒绝 / 无终端 fail-closed）、`click_element` 与 `click` 的分支、
Escape 中止与崩溃自愈、光标位图判定、可选字段传 `null` 的容错。

工具层测试走真实的 MCP 会话（`mcp.NewInMemoryTransports`），
所以 **JSON schema 校验也会真实发生** —— `null` 那个 bug 正是这样被抓出来的。

引擎协议用一个内嵌的 Go 假 helper 测试（`client_test.go`），
不依赖真实 Windows 环境。

### 已验证

| 项 | 结果 |
|---|---|
| stdio | ✅ 14 工具，真实窗口 |
| Streamable HTTP | ✅ 会话管理 + 工具调用 |
| SSE | ✅ endpoint 握手 |
| 读界面 | ✅ 真实无障碍树 |
| 截图 | ✅ MCP image 块（~270KB JPEG） |
| 审批 | ✅ 接管，客户端无需支持 elicitation |
| 交叉编译 | ✅ 六个平台全部通过 |
| 无孤儿进程 | ✅ helper 靠 `--parent-pid` 自行退出（无需 Job Object） |
| 强杀不丢光标 | ✅ 父进程被强杀后光标仍正常 |
| 优雅关闭 | ✅ 先 close 握手再退出，不留光标残留 |
| 崩溃自愈 | ✅ 杀掉引擎后下次调用自动恢复 |
| Escape 中止 | ✅ 识别退出码 130，本轮内拒绝后续调用 |
