package mcpserver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zhilv666/cua-mcp/internal/engine"
	"github.com/zhilv666/cua-mcp/internal/sky"
)

func newGate(t *testing.T, answer string, auto bool) (*Approver, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	a := NewApprover(path, auto, nil)
	a.Prompt = func(context.Context, string) (string, error) {
		return answer, nil
	}
	return a, path
}

func req(app string) engine.ApprovalRequest {
	return engine.ApprovalRequest{App: app, DisplayName: app, AllowPersistentApproval: true}
}

func TestDecideAlwaysPersistsToSharedFile(t *testing.T) {
	a, path := newGate(t, "a", false)

	got, err := a.Decide(context.Background(), req("testapp.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allow || !got.Persist {
		t.Fatalf("期望允许且持久化，得到 %+v", got)
	}
	// 必须落到与 Codex 共用的那份文件里，格式要能被解析回来。
	if apps := sky.ReadAllowlist(path); !sky.ContainsApp(apps, "testapp.exe") {
		t.Errorf("白名单未写入: %#v", apps)
	}
}

func TestDecideOnceDoesNotPersist(t *testing.T) {
	a, path := newGate(t, "y", false)

	got, err := a.Decide(context.Background(), req("once.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allow || got.Persist {
		t.Fatalf("期望允许但不持久化，得到 %+v", got)
	}
	if apps := sky.ReadAllowlist(path); len(apps) != 0 {
		t.Errorf("不应写入白名单，得到 %#v", apps)
	}
}

func TestDecideDenies(t *testing.T) {
	a, path := newGate(t, "n", false)

	got, err := a.Decide(context.Background(), req("bad.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allow {
		t.Fatalf("期望拒绝，得到 %+v", got)
	}
	if apps := sky.ReadAllowlist(path); len(apps) != 0 {
		t.Errorf("拒绝不应写入白名单，得到 %#v", apps)
	}
}

// 拿不到终端时必须拒绝 —— 绝不能默默放行。
func TestDecideNoConsoleFailsClosed(t *testing.T) {
	a := NewApprover(filepath.Join(t.TempDir(), "config.toml"), false, nil)
	a.Prompt = func(context.Context, string) (string, error) {
		return "", ErrNoConsole
	}
	got, err := a.Decide(context.Background(), req("x.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allow {
		t.Fatal("无终端时必须拒绝")
	}
}

func TestAutoApprovesEverything(t *testing.T) {
	a, path := newGate(t, "", true)
	got, err := a.Decide(context.Background(), req("anything.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allow || !got.Persist {
		t.Fatalf("auto 模式应放行并持久化，得到 %+v", got)
	}
	if apps := sky.ReadAllowlist(path); !sky.ContainsApp(apps, "anything.exe") {
		t.Errorf("auto 模式应写入白名单: %#v", apps)
	}
}

func TestWhitelistedAppSkipsPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := sky.Grant(path, "known.exe"); err != nil {
		t.Fatal(err)
	}
	prompted := false
	a := NewApprover(path, false, nil)
	a.Prompt = func(context.Context, string) (string, error) {
		prompted = true
		return "n", nil
	}
	got, err := a.Decide(context.Background(), req("known.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allow {
		t.Fatalf("白名单内的 app 应放行，得到 %+v", got)
	}
	if prompted {
		t.Error("白名单内的 app 不应再询问")
	}
}

// 引擎不允许持久化时，回答 "a" 也不该写白名单，
// 否则引擎会认为用户授权了持久化却查不到记录。
func TestAlwaysDowngradesWhenEngineDisallowsPersist(t *testing.T) {
	a, path := newGate(t, "a", false)

	got, err := a.Decide(context.Background(), engine.ApprovalRequest{
		App:                     "nopersist.exe",
		DisplayName:             "NoPersist",
		AllowPersistentApproval: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allow {
		t.Fatalf("应放行，得到 %+v", got)
	}
	if got.Persist {
		t.Error("引擎不允许持久化时不应标记 Persist")
	}
	if apps := sky.ReadAllowlist(path); len(apps) != 0 {
		t.Errorf("不应写入白名单，得到 %#v", apps)
	}
}

func TestGrantRejectsEmpty(t *testing.T) {
	a, _ := newGate(t, "", false)
	if err := a.Grant(""); err == nil {
		t.Error("空 app 名应报错")
	}
}

func TestDecidePropagatesPromptError(t *testing.T) {
	boom := errors.New("终端炸了")
	a := NewApprover(filepath.Join(t.TempDir(), "config.toml"), false, nil)
	a.Prompt = func(context.Context, string) (string, error) { return "", boom }
	// Prompt 出错时按拒绝处理 —— 任何拿不到答复的情况都不能放行。
	got, err := a.Decide(context.Background(), req("x.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allow {
		t.Error("出错时应拒绝")
	}
}

// session 里的批准不应写盘，但同一进程内第二次调用应直接放行。
func TestSessionApprovalIsRemembered(t *testing.T) {
	a, path := newGate(t, "y", false)
	first, err := a.Decide(context.Background(), req("sess.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Allow {
		t.Fatal("首次应放行")
	}

	// 换成「拒绝」的 prompt；若还会询问，就会拒绝，说明没记住。
	a.Prompt = func(context.Context, string) (string, error) { return "n", nil }
	second, err := a.Decide(context.Background(), req("sess.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Allow {
		t.Error("同一会话内第二次应直接放行")
	}
	if apps := sky.ReadAllowlist(path); len(apps) != 0 {
		t.Errorf("仅本次的批准不应写盘: %#v", apps)
	}
}

func TestGrantAndRevoke(t *testing.T) {
	a, path := newGate(t, "", false)
	if err := a.Grant("g.exe"); err != nil {
		t.Fatal(err)
	}
	if apps := sky.ReadAllowlist(path); !sky.ContainsApp(apps, "g.exe") {
		t.Fatalf("Grant 未生效: %#v", apps)
	}
	if err := a.Revoke("g.exe"); err != nil {
		t.Fatal(err)
	}
	if apps := sky.ReadAllowlist(path); sky.ContainsApp(apps, "g.exe") {
		t.Errorf("Revoke 未生效: %#v", apps)
	}
}
