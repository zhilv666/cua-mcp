package sky

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func tmpFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestReadAllowlist(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"空文件", "", nil},
		{"无 apps 段", "[foo]\nbar = 1\n", nil},
		{"apps 段无 allowed", "[apps]\n", nil},
		{"单项", "[apps]\nallowed = [\"zed.exe\"]\n", []string{"zed.exe"}},
		{"多项", "[apps]\nallowed = [\"a.exe\", \"b.exe\"]\n", []string{"a.exe", "b.exe"}},
		{"带空格的项", "[apps]\nallowed = [\"a b.exe\"]\n", []string{"a b.exe"}},
		{"后有其他段", "[apps]\nallowed = [\"a.exe\"]\n\n[other]\nx = 1\n", []string{"a.exe"}},
		{"多行数组", "[apps]\nallowed = [\n  \"a.exe\",\n  \"b.exe\",\n]\n", []string{"a.exe", "b.exe"}},
		{"转义引号", `[apps]` + "\n" + `allowed = ["a\"b.exe"]` + "\n", []string{`a"b.exe`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadAllowlist(tmpFile(t, tc.body))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("得到 %#v，期望 %#v", got, tc.want)
			}
		})
	}
}

func TestReadAllowlistMissingFile(t *testing.T) {
	got := ReadAllowlist(filepath.Join(t.TempDir(), "nope.toml"))
	if got != nil {
		t.Errorf("文件不存在应返回 nil，得到 %#v", got)
	}
}

func TestWriteAllowlistCreatesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "computer-use", "config.toml")
	if err := WriteAllowlist(p, []string{"zed.exe"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadAllowlist(p); !reflect.DeepEqual(got, []string{"zed.exe"}) {
		t.Errorf("往返失败: %#v", got)
	}
}

// 最关键的一条：写入时必须保留文件里其余的内容。
func TestWriteAllowlistPreservesOtherSections(t *testing.T) {
	body := "[apps]\nallowed = [\"a.exe\"]\n\n[other]\nx = 1\ny = \"keep me\"\n"
	p := tmpFile(t, body)
	if err := WriteAllowlist(p, []string{"c.exe"}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(p)

	if !strings.Contains(string(out), "[other]") || !strings.Contains(string(out), "keep me") {
		t.Errorf("其余段被破坏:\n%s", out)
	}
	if got := ReadAllowlist(p); !reflect.DeepEqual(got, []string{"c.exe"}) {
		t.Errorf("读取得到 %#v", got)
	}
}

func TestWriteAllowlistPreservesNoTrailingNewline(t *testing.T) {
	// Spark 自己写的文件可能没有末尾换行，不应因此丢内容。
	p := tmpFile(t, `[apps]\nallowed = ["a.exe"]`)
	if err := WriteAllowlist(p, []string{"b.exe"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadAllowlist(p); !reflect.DeepEqual(got, []string{"b.exe"}) {
		t.Errorf("得到 %#v", got)
	}
}

func TestWriteAllowlistAddsSection(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"空文件", ""},
		{"只有其他段", "[foo]\nbar=1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tmpFile(t, tc.body)
			if err := WriteAllowlist(p, []string{"e.exe"}); err != nil {
				t.Fatal(err)
			}
			if got := ReadAllowlist(p); !reflect.DeepEqual(got, []string{"e.exe"}) {
				t.Errorf("得到 %#v", got)
			}
		})
	}
}

func TestWriteAllowlistDedupesAndSorts(t *testing.T) {
	p := tmpFile(t, "")
	if err := WriteAllowlist(p, []string{"z.exe", "a.exe", "z.exe"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadAllowlist(p); !reflect.DeepEqual(got, []string{"a.exe", "z.exe"}) {
		t.Errorf("得到 %#v，期望去重并排序", got)
	}
}

func TestGrantIsIdempotent(t *testing.T) {
	p := tmpFile(t, `[apps]`+"\n"+`allowed = ["a.exe"]`+"\n")
	if err := Grant(p, "a.exe"); err != nil {
		t.Fatal(err)
	}
	if got := ReadAllowlist(p); !reflect.DeepEqual(got, []string{"a.exe"}) {
		t.Errorf("重复 Grant 后应为 %#v，得到 %#v", []string{"a.exe"}, got)
	}
}

func TestRevoke(t *testing.T) {
	p := tmpFile(t, `[apps]`+"\n"+`allowed = ["a.exe", "b.exe"]`+"\n")
	if err := Revoke(p, "a.exe"); err != nil {
		t.Fatal(err)
	}
	if got := ReadAllowlist(p); !reflect.DeepEqual(got, []string{"b.exe"}) {
		t.Errorf("得到 %#v", got)
	}
	if err := Revoke(p, "does-not-exist.exe"); err != nil {
		t.Errorf("撤销不存在的项不应报错: %v", err)
	}
}

func TestContainsApp(t *testing.T) {
	apps := []string{"a.exe", "b.exe"}
	if !ContainsApp(apps, "a.exe") || ContainsApp(apps, "c.exe") {
		t.Error("ContainsApp 结果错误")
	}
}
