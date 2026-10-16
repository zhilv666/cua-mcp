// Package sky 负责定位 Codex 运行时与读写审批白名单。
package sky

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Helper 描述定位到的 computer use 引擎。
type Helper struct {
	// Root 是找到引擎的那棵 cua_node 树（或等效目录）。
	Root string
	// Exe 是 codex-computer-use.exe 的完整路径。
	Exe string
}

var errNotFound = errors.New(
	"找不到 Codex 的 computer use 引擎 (codex-computer-use.exe)。\n" +
		"  它随 Codex 桌面版分发，没有独立的下载地址。请确认已安装 Codex，\n" +
		"  或用 CUA_HELPER_PATH 直接指定该文件的路径",
)

// manifest 是运行时清单，用来说明这份归档是什么平台。
type manifest struct {
	Platform              string `json:"platform"`
	Arch                  string `json:"arch"`
	RuntimeArchiveName    string `json:"runtime_archive_name"`
	RuntimeArchiveVersion string `json:"runtime_archive_version"`
}

// 引擎在包内的相对路径（架构不同名字不同）。
var engineRelPaths = []string{
	filepath.Join("bin", "node_modules", "@oai", "sky", "bin", "windows"), // 主实现
	filepath.Join("bin", "node_modules", "@oai", "cua", "bin", "windows"), // 副本
}

// Find 定位 codex-computer-use.exe。
//
// 两个环境变量都是**权威**的 —— 显式指定就照办，不做自动查找；
// 指定的位置无效时直接报错，而不是悄悄回落到其他安装。
// 悄悄回落会让「我明明指了这个，它却用了那个」变得难以察觉。
//
//	CUA_HELPER_PATH   直接指向 exe，优先级最高
//	CUA_NODE_RUNTIME  指向一棵 cua_node 树
//
// 都没设时，扫描各已知安装位置（见 knownRoots），
// 找到多份取版本号最新的一份（manifest.json 里的 runtime_archive_name
// 内含时间戳）。
func Find() (*Helper, error) {
	// 1. 直接指定的 exe
	if p := strings.TrimSpace(os.Getenv("CUA_HELPER_PATH")); p != "" {
		abs, err := filepath.Abs(expandHome(p))
		if err != nil {
			return nil, fmt.Errorf("CUA_HELPER_PATH 无效: %w", err)
		}
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			return nil, fmt.Errorf("CUA_HELPER_PATH 指向的文件不存在或不是文件: %s", abs)
		}
		return &Helper{Root: filepath.Dir(filepath.Dir(abs)), Exe: abs}, nil
	}

	// 2. 指定的运行时树
	if p := strings.TrimSpace(os.Getenv("CUA_NODE_RUNTIME")); p != "" {
		root := expandHome(p)
		h, err := inspect(root)
		if err != nil {
			return nil, fmt.Errorf(
				"CUA_NODE_RUNTIME 指向的目录里找不到引擎: %s\n"+
					"  期望结构：<该目录>/bin/node_modules/@oai/sky/bin/windows/codex-computer-use.exe", root)
		}
		return h, nil
	}

	// 3. 自动查找
	type candidate struct {
		helper  *Helper
		version string
	}
	seen := map[string]bool{}
	var found []candidate
	add := func(root string) {
		if seen[root] {
			return
		}
		seen[root] = true
		h, err := inspect(root)
		if err != nil {
			return
		}
		found = append(found, candidate{helper: h, version: readVersion(root)})
	}

	roots := knownRoots()
	for _, r := range roots {
		add(r)
	}
	// 有些安装把 cua_node 埋在更深一层
	for _, r := range roots {
		for _, deep := range childDirsNamed(r, "cua_node") {
			add(deep)
		}
	}

	if len(found) == 0 {
		return nil, errNotFound
	}
	// 版本字符串内含时间戳，字典序即时间序。
	sort.Slice(found, func(i, j int) bool { return found[i].version < found[j].version })
	return found[len(found)-1].helper, nil
}

// Candidate 是 Find 考察过的一个位置，供诊断用。
type Candidate struct {
	Path  string // 被考察的目录或文件
	Found bool   // 这里是否找到了引擎
	Exe   string // 找到时的完整路径
}

// Candidates 列出 Find 会考察的所有位置及其结果。
//
// 自动查找涉及多个安装形态，出问题时只看到「找不到」无从下手 ——
// 这个函数把每个候选都摊开，供 `cua-mcp doctor` 展示。
func Candidates() []Candidate {
	var out []Candidate

	if p := strings.TrimSpace(os.Getenv("CUA_HELPER_PATH")); p != "" {
		abs, err := filepath.Abs(expandHome(p))
		c := Candidate{Path: "CUA_HELPER_PATH = " + p}
		if err == nil {
			if _, err := os.Stat(abs); err == nil {
				c.Found, c.Exe = true, abs
			} else {
				c.Path += "（文件不存在）"
			}
		}
		return append(out, c)
	}

	seen := map[string]bool{}
	consider := func(root string) {
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		c := Candidate{Path: root}
		if h, err := inspect(root); err == nil {
			c.Found, c.Exe = true, h.Exe
		}
		out = append(out, c)

		// inspect 也会看 root/cua_node，一并展示
		inner := filepath.Join(root, "cua_node")
		if !seen[inner] {
			seen[inner] = true
			ic := Candidate{Path: inner}
			if h, err := inspect(inner); err == nil {
				ic.Found, ic.Exe = true, h.Exe
			}
			out = append(out, ic)
		}
	}

	if p := strings.TrimSpace(os.Getenv("CUA_NODE_RUNTIME")); p != "" {
		consider(expandHome(p))
	}
	for _, r := range knownRoots() {
		consider(r)
	}
	return out
}

// knownRoots 列出所有可能挂着 cua_node 树的目录。
func knownRoots() []string {
	var roots []string

	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		if home, err := os.UserHomeDir(); err == nil {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
	}

	if localAppData != "" {
		codex := filepath.Join(localAppData, "OpenAI", "Codex")
		// 标准位置：runtimes/cua_node/<hash>
		for _, d := range childDirs(filepath.Join(codex, "runtimes", "cua_node")) {
			roots = append(roots, d)
		}
		// 少数安装直接把 cua_node 放在 runtimes 下
		for _, d := range childDirs(filepath.Join(codex, "runtimes")) {
			roots = append(roots, d)
		}
		// 插件缓存：plugins/cache/*/*/...
		for _, p := range childDirs(filepath.Join(codex, "plugins")) {
			roots = append(roots, p)
		}
	}

	// Codex 桌面版的安装目录（Store 版在 WindowsApps，桌面版在 Programs）
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			filepath.Join(home, "AppData", "Local", "Programs", "OpenAI", "Codex"),
		)
	}
	for _, base := range []string{
		filepath.Join(os.Getenv("ProgramFiles"), "WindowsApps"),
		filepath.Join(os.Getenv("ProgramW6432"), "WindowsApps"),
	} {
		for _, d := range globDirs(filepath.Join(base, "OpenAI.Codex_*", "app", "resources", "cua_node")) {
			roots = append(roots, d)
		}
	}

	return roots
}

// inspect 判断 root 是否含可用的引擎文件。
//
// root 可能是：
//   - 一棵 cua_node 树（含 manifest.json 与 bin/node_modules/...）
//   - 其父目录（下面还有一层 cua_node）
func inspect(root string) (*Helper, error) {
	// root 本身像 cua_node 树
	if exe, err := findEngineExe(root); err == nil {
		return &Helper{Root: root, Exe: exe}, nil
	}
	// root/cua_node 是
	inner := filepath.Join(root, "cua_node")
	if exe, err := findEngineExe(inner); err == nil {
		return &Helper{Root: inner, Exe: exe}, nil
	}
	return nil, fs.ErrNotExist
}

// findEngineExe 在给定的 cua_node 树里找引擎 exe。
func findEngineExe(root string) (string, error) {
	if root == "" {
		return "", fs.ErrNotExist
	}
	// 架构相关的名字优先
	names := []string{"codex-computer-use.exe"}
	if runtime.GOARCH == "arm64" {
		names = append([]string{"codex-computer-use-arm64.exe"}, names...)
	}

	for _, rel := range engineRelPaths {
		for _, n := range names {
			p := filepath.Join(root, rel, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	return "", fs.ErrNotExist
}

// readVersion 取运行时版本（用于在多份安装里挑最新）。
func readVersion(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return ""
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if m.RuntimeArchiveName != "" {
		return m.RuntimeArchiveName
	}
	return m.RuntimeArchiveVersion
}

// ---------- 目录遍历小工具 ----------

func childDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// childDirsNamed 找 dir 下（限一层）名为 name 的子目录。
func childDirsNamed(dir, name string) []string {
	var out []string
	for _, d := range childDirs(dir) {
		if strings.EqualFold(filepath.Base(d), name) {
			out = append(out, d)
		}
	}
	return out
}

// globDirs 展开 glob 并只保留目录。
func globDirs(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			out = append(out, m)
		}
	}
	return out
}
