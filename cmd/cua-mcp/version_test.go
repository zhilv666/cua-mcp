package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"版本", 4},   // 每个汉字两列
		{"构建时间", 8}, // 四个汉字
		{"v1.0.0", 6},
		{"提交 abc", 8}, // 2*2 + 空格1 + 3
	}
	for _, c := range cases {
		if got := displayWidth(c.in); got != c.want {
			t.Errorf("displayWidth(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// 所有标签的显示宽度必须一致，否则值的起始列会对不齐。
func TestVersionTextAligned(t *testing.T) {
	v := versionInfo{
		Name:    "cua-mcp",
		Version: "v1.0.0",
		Commit:  "abc1234",
		OS:      "windows",
		Arch:    "amd64",
		Time:    "2026-10-16T10:00:00+0800",
	}
	out := versionText(v)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("应有 5 行，得到 %d：\n%s", len(lines), out)
	}

	// 每行「值」的起始列应相同。
	// 值本身不含空格，故按「最后一个空格之后」定位即可。
	cols := map[int]bool{}
	for _, ln := range lines {
		i := strings.LastIndex(ln, " ")
		if i < 0 {
			t.Fatalf("行里没有分隔：%q", ln)
		}
		cols[displayWidth(ln[:i+1])] = true
	}
	if len(cols) != 1 {
		t.Errorf("各行的值起始列不一致：%v\n%s", cols, out)
	}
}

// 取不到的字段不该占行。
func TestVersionTextSkipsEmpty(t *testing.T) {
	out := versionText(versionInfo{Version: "v1.0.0", OS: "linux", Arch: "amd64"})
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Errorf("只应输出 3 行，得到：\n%s", out)
	}
	for _, ln := range lines {
		if strings.Contains(ln, "构建时间") {
			t.Error("构建时间为空时不该出现该行")
		}
	}
}

func TestVersionJSON(t *testing.T) {
	v := versionInfo{
		Name:    "cua-mcp",
		Version: "v1.0.0",
		Commit:  "abc1234",
		OS:      "windows",
		Arch:    "amd64",
		Time:    "2026-10-16T10:00:00+0800",
	}
	s, err := versionJSON(v)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatalf("输出不是合法 JSON: %v\n%s", err, s)
	}
	want := map[string]string{
		"name": "cua-mcp", "version": "v1.0.0", "commit": "abc1234",
		"os": "windows", "arch": "amd64", "time": "2026-10-16T10:00:00+0800",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q，期望 %q", k, got[k], w)
		}
	}
	if strings.Contains(s, "\n") {
		t.Error("JSON 应单行输出，便于脚本消费")
	}
}

// 未注入时应回落到运行时信息，而不是空值。
func TestCollectVersionFallsBack(t *testing.T) {
	v := collectVersion()
	if v.Version == "" {
		t.Error("版本不该为空（应回落为 dev 或伪版本）")
	}
	if v.OS == "" || v.Arch == "" {
		t.Errorf("平台/架构应回落到运行时值，得到 %+v", v)
	}
	if v.Name != "cua-mcp" {
		t.Errorf("程序名应为 cua-mcp，得到 %q", v.Name)
	}
}
