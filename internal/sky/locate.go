// Package sky 负责定位 Codex 运行时与读写审批白名单。
package sky

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Helper 描述定位到的 computer use 引擎。
type Helper struct {
	// RuntimeRoot 是 cua_node 运行时目录，形如
	// %LOCALAPPDATA%\OpenAI\Codex\runtimes\cua_node\<hash>
	RuntimeRoot string
	// Exe 是 codex-computer-use.exe 的完整路径。
	Exe string
}

var errNotFound = errors.New(
	"找不到 Codex 的 computer use 引擎。请确认已安装 Codex 桌面版，" +
		"或用 CUA_HELPER_PATH 环境变量直接指定 codex-computer-use.exe",
)

// manifest 是运行时清单，用来说明这份归档是什么平台。
type manifest struct {
	Platform              string `json:"platform"`
	Arch                  string `json:"arch"`
	RuntimeArchiveName    string `json:"runtime_archive_name"`
	RuntimeArchiveVersion string `json:"runtime_archive_version"`
}

// Find 定位 codex-computer-use.exe。
//
// 查找顺序：
//  1. CUA_HELPER_PATH 环境变量（直接指向 exe）
//  2. CUA_NODE_RUNTIME 环境变量（指向运行时根）
//  3. %LOCALAPPDATA%\OpenAI\Codex\runtimes\cua_node\* 下的每个运行时
//
// 第 3 步会选版本最新的一份。版本号取自 manifest.json 的
// runtime_archive_name，形如 cua-node-0.0.29-20261005054403-<hash>-windows-x64.zip。
func Find() (*Helper, error) {
	if p := strings.TrimSpace(os.Getenv("CUA_HELPER_PATH")); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("CUA_HELPER_PATH 无效: %w", err)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("CUA_HELPER_PATH 指向的文件不存在: %s", abs)
		}
		return &Helper{RuntimeRoot: filepath.Dir(filepath.Dir(abs)), Exe: abs}, nil
	}

	var roots []string
	if p := strings.TrimSpace(os.Getenv("CUA_NODE_RUNTIME")); p != "" {
		roots = append(roots, p)
	}
	roots = append(roots, candidateRuntimeRoots()...)

	type candidate struct {
		helper  *Helper
		version string
	}
	var found []candidate
	for _, root := range roots {
		h, err := inspect(root)
		if err != nil {
			continue
		}
		found = append(found, candidate{helper: h, version: readVersion(root)})
	}
	if len(found) == 0 {
		return nil, errNotFound
	}
	// 版本字符串内含时间戳，字典序即时间序。
	sort.Slice(found, func(i, j int) bool { return found[i].version < found[j].version })
	return found[len(found)-1].helper, nil
}

func candidateRuntimeRoots() []string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		localAppData = filepath.Join(home, "AppData", "Local")
	}

	var out []string
	for _, base := range []string{
		filepath.Join(localAppData, "OpenAI", "Codex", "runtimes", "cua_node"),
		filepath.Join(localAppData, "OpenAI", "Codex", "runtimes"),
	} {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, filepath.Join(base, e.Name()))
			}
		}
	}
	return out
}

// inspect 判断一个目录是否是含 Windows helper 的运行时。
func inspect(root string) (*Helper, error) {
	// 引擎名随架构变化：arm64 用 -arm64 后缀。
	name := "codex-computer-use.exe"
	if runtime.GOARCH == "arm64" {
		name = "codex-computer-use-arm64.exe"
	}
	exe := filepath.Join(root, "bin", "node_modules", "@oai", "sky", "bin", "windows", name)
	if _, err := os.Stat(exe); err != nil {
		// 回落：非 arm64 机器上也可能只有基础名
		fallback := filepath.Join(root, "bin", "node_modules", "@oai", "sky", "bin", "windows", "codex-computer-use.exe")
		if _, err2 := os.Stat(fallback); err2 != nil {
			return nil, err
		}
		exe = fallback
	}
	return &Helper{RuntimeRoot: root, Exe: exe}, nil
}

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
