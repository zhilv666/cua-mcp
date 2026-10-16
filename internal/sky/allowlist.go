package sky

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// DefaultAllowlistPath 返回 Codex 自己那份审批白名单的位置。
//
// 白名单不另建一份：Spark/Sky 引擎自己就用这个文件记录长期批准，
// 且在每次操作前查它。读写同一份才能保证「Codex App 批准过的 app，
// 本服务器也认」，反之亦然。文件内容是：
//
//	[apps]
//	allowed = ["zed.exe"]
func DefaultAllowlistPath() string {
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".codex", "computer-use", "config.toml")
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexHome, "computer-use", "config.toml")
}

var (
	// 段内的 allowed = [ ... ]
	reAllowedList = regexp.MustCompile(`(?s)allowed\s*=\s*\[(.*?)\]`)
	// 双引号字符串（含转义）
	reQuoted = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	// 段头，如 [apps]
	reSectionHead = regexp.MustCompile(`^\s*\[`)
)

// section 返回 [name] 段的正文（不含段头），以及该段正文在原文中的起止下标。
// ok 为 false 表示没有这个段。
//
// 手写扫描而不用正则：Go 的 regexp 是 RE2，不支持「一直匹配到下一个段头
// 或文末」所需的前瞻。
func section(text, name string) (body string, start, end int, ok bool) {
	head := "[" + name + "]"
	lines := strings.SplitAfter(text, "\n")

	// offset[i] 是第 i 行在 text 中的起始下标。
	offset := make([]int, len(lines)+1)
	for i, line := range lines {
		offset[i+1] = offset[i] + len(line)
	}

	for i, line := range lines {
		if strings.TrimSpace(line) != head {
			continue
		}
		start = offset[i+1] // 段头之后
		end = len(text)
		for j := i + 1; j < len(lines); j++ {
			if reSectionHead.MatchString(lines[j]) {
				end = offset[j] // 下一个段头之前
				break
			}
		}
		return text[start:end], start, end, true
	}
	return "", 0, 0, false
}

// ReadAllowlist 读取白名单中的 app 标识。
//
// 解析失败一律返回空表而不报错：白名单读不出来时应当保守
// （视作全未批准，逐个询问），而不是让整个服务起不来。
func ReadAllowlist(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	body, _, _, ok := section(string(data), "apps")
	if !ok {
		return nil
	}
	list := reAllowedList.FindStringSubmatch(body)
	if list == nil {
		return nil
	}

	var apps []string
	for _, m := range reQuoted.FindAllStringSubmatch(list[1], -1) {
		var unescaped string
		// 借 JSON 处理转义，与 TOML 的基础字符串语义一致。
		if err := json.Unmarshal([]byte(`"`+m[1]+`"`), &unescaped); err != nil {
			continue
		}
		apps = append(apps, unescaped)
	}
	return apps
}

// WriteAllowlist 把白名单写回文件，其余内容原样保留。
//
// 只动 allowed 数组：有 [apps] 段就替换，没有就补一个。
func WriteAllowlist(path string, apps []string) error {
	unique := dedupeSorted(apps)

	var quoted []string
	for _, a := range unique {
		b, err := json.Marshal(a)
		if err != nil {
			return fmt.Errorf("无法序列化 app 名 %q: %w", a, err)
		}
		quoted = append(quoted, string(b))
	}
	line := "allowed = [" + strings.Join(quoted, ", ") + "]"

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	text := string(existing)

	body, start, end, hasSection := section(text, "apps")

	var next string
	switch {
	case hasSection && reAllowedList.MatchString(body):
		// 段内已有 allowed → 就地替换。
		replaced := reAllowedList.ReplaceAllString(body, "allowed = ["+strings.Join(quoted, ", ")+"]")
		next = text[:start] + replaced + text[end:]
	case hasSection:
		// 有 [apps] 段但没有 allowed → 插在段头之后。
		next = text[:start] + line + "\n" + text[start:]
	default:
		// 没有该段 → 追加一个。
		trimmed := strings.TrimRight(text, " \t\r\n")
		if trimmed != "" {
			next = trimmed + "\n\n[apps]\n" + line + "\n"
		} else {
			next = "[apps]\n" + line + "\n"
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}

// ContainsApp 判断白名单是否已包含某 app。
func ContainsApp(apps []string, app string) bool {
	return slices.Contains(apps, app)
}

// Grant 把 app 加入长期白名单（幂等）。
func Grant(path, app string) error {
	apps := ReadAllowlist(path)
	if ContainsApp(apps, app) {
		return nil
	}
	return WriteAllowlist(path, append(apps, app))
}

// Revoke 把 app 移出长期白名单。
func Revoke(path, app string) error {
	apps := ReadAllowlist(path)
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		if a != app {
			out = append(out, a)
		}
	}
	if len(out) == len(apps) {
		return nil
	}
	return WriteAllowlist(path, out)
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
