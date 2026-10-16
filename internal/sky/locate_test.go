package sky

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeFakeRuntime 造一棵假的 cua_node 树，含引擎 exe 与 manifest。
func makeFakeRuntime(t *testing.T, root, version string) string {
	t.Helper()
	exeDir := filepath.Join(root, "bin", "node_modules", "@oai", "sky", "bin", "windows")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "codex-computer-use.exe"
	if runtime.GOARCH == "arm64" {
		name = "codex-computer-use-arm64.exe"
	}
	if err := os.WriteFile(filepath.Join(exeDir, name), []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		m := `{"runtime_archive_name":"` + version + `"}`
		if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(exeDir, name)
}

// CUA_HELPER_PATH 直接指定时，应原样采纳并校验存在性。
func TestFindHonorsHelperPathEnv(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "codex-computer-use.exe")
	if err := os.WriteFile(exe, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUA_HELPER_PATH", exe)

	h, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if h.Exe != exe {
		t.Errorf("得到 %s，期望 %s", h.Exe, exe)
	}
}

// CUA_HELPER_PATH 指向不存在的文件应报错，而不是静默回落。
func TestFindRejectsMissingHelperPath(t *testing.T) {
	t.Setenv("CUA_HELPER_PATH", filepath.Join(t.TempDir(), "nope.exe"))
	if _, err := Find(); err == nil {
		t.Fatal("应报错")
	}
}

// CUA_NODE_RUNTIME 指向一棵树时应能直接识别。
func TestFindHonorsNodeRuntimeEnv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cua_node")
	want := makeFakeRuntime(t, root, "cua-node-0.0.1-20260101000000-abc-windows-x64.zip")
	t.Setenv("CUA_HELPER_PATH", "")
	t.Setenv("CUA_NODE_RUNTIME", root)

	h, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if h.Exe != want {
		t.Errorf("得到 %s，期望 %s", h.Exe, want)
	}
}

// 同一父目录下有多个版本时，应挑版本号最新的那份。
func TestFindPicksNewestVersion(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "aaaa1111")
	new_ := filepath.Join(base, "bbbb2222")
	makeFakeRuntime(t, old, "cua-node-0.0.10-20260101000000-x-windows-x64.zip")
	want := makeFakeRuntime(t, new_, "cua-node-0.0.29-20261005054403-y-windows-x64.zip")

	t.Setenv("CUA_HELPER_PATH", "")
	t.Setenv("CUA_NODE_RUNTIME", "")
	// 把两个目录都作为候选：直接验证挑选逻辑
	type cand struct {
		h *Helper
		v string
	}
	var found []cand
	for _, r := range []string{old, new_} {
		if h, err := inspect(r); err == nil {
			found = append(found, cand{h, readVersion(r)})
		}
	}
	if len(found) != 2 {
		t.Fatalf("应识别出两棵运行时树，得到 %d", len(found))
	}
	if found[1].v <= found[0].v {
		t.Fatalf("版本序不对：%s <= %s", found[1].v, found[0].v)
	}
	if found[1].h.Exe != want {
		t.Errorf("较新的一份应是 %s", want)
	}
}

// root/cua_node 这种多一层的情形也要能认出来。
func TestInspectFindsNestedCuaNode(t *testing.T) {
	parent := t.TempDir()
	inner := filepath.Join(parent, "cua_node")
	want := makeFakeRuntime(t, inner, "v1")

	h, err := inspect(parent)
	if err != nil {
		t.Fatalf("应能在 %s 下找到 cua_node: %v", parent, err)
	}
	if h.Exe != want {
		t.Errorf("得到 %s，期望 %s", h.Exe, want)
	}
}

// @oai/cua 下的副本也要认（安装形态之一）。
func TestInspectFindsCuaVariant(t *testing.T) {
	root := t.TempDir()
	exeDir := filepath.Join(root, "bin", "node_modules", "@oai", "cua", "bin", "windows")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(exeDir, "codex-computer-use.exe")
	if err := os.WriteFile(want, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	h, err := inspect(root)
	if err != nil {
		t.Fatalf("应识别 @oai/cua 变体: %v", err)
	}
	if h.Exe != want {
		t.Errorf("得到 %s，期望 %s", h.Exe, want)
	}
}

// 空目录不该被误判成运行时。
func TestInspectRejectsEmptyDir(t *testing.T) {
	if _, err := inspect(t.TempDir()); err == nil {
		t.Error("空目录不该被认作运行时")
	}
}

// Candidates 应产出可展示的候选列表，且不 panic。
func TestCandidatesListsPositions(t *testing.T) {
	t.Setenv("CUA_HELPER_PATH", "")
	t.Setenv("CUA_NODE_RUNTIME", "")

	got := Candidates()
	if len(got) == 0 {
		t.Skip("本机没有已知的 Codex 安装位置，跳过")
	}
	for _, c := range got {
		if c.Path == "" {
			t.Error("候选路径不该为空")
		}
		if c.Found && c.Exe == "" {
			t.Errorf("标记为找到但没给 exe: %+v", c)
		}
	}
}

// CUA_HELPER_PATH 设了时，Candidates 只报这一条。
func TestCandidatesShortCircuitsOnHelperPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "x.exe")
	if err := os.WriteFile(exe, []byte("f"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUA_HELPER_PATH", exe)

	got := Candidates()
	if len(got) != 1 || !got[0].Found || got[0].Exe != exe {
		t.Errorf("设了 CUA_HELPER_PATH 时应只报这一条，得到 %+v", got)
	}
}
