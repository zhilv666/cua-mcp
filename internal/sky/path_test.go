package sky

import (
	"os"
	"path/filepath"
	"testing"
)

// 优先级：显式传入 > 环境变量 > Codex 默认位置。
func TestResolveAllowlistPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("取不到主目录")
	}
	def := filepath.Join(home, ".codex", "computer-use", "config.toml")

	t.Run("显式路径优先", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, filepath.Join(home, "from-env.toml"))
		got := ResolveAllowlistPath(filepath.Join(home, "explicit.toml"))
		if got != filepath.Join(home, "explicit.toml") {
			t.Errorf("得到 %s", got)
		}
	})

	t.Run("无显式则用环境变量", func(t *testing.T) {
		want := filepath.Join(home, "from-env.toml")
		t.Setenv(AllowlistPathEnv, want)
		if got := ResolveAllowlistPath(""); got != want {
			t.Errorf("得到 %s，期望 %s", got, want)
		}
	})

	t.Run("都没有则用默认位置", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, "")
		t.Setenv("CODEX_HOME", "")
		if got := ResolveAllowlistPath(""); got != def {
			t.Errorf("得到 %s，期望 %s", got, def)
		}
	})

	t.Run("空白显式值视为未指定", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, "")
		t.Setenv("CODEX_HOME", "")
		if got := ResolveAllowlistPath("   "); got != def {
			t.Errorf("得到 %s，期望回落到默认 %s", got, def)
		}
	})

	t.Run("环境变量两边空白会被裁掉", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, "  "+filepath.Join(home, "x.toml")+"  ")
		if got := ResolveAllowlistPath(""); got != filepath.Join(home, "x.toml") {
			t.Errorf("得到 %q", got)
		}
	})
}

// CODEX_HOME 决定默认位置。
func TestDefaultAllowlistPathHonorsCodexHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", tmp)
	want := filepath.Join(tmp, "computer-use", "config.toml")
	if got := DefaultAllowlistPath(); got != want {
		t.Errorf("得到 %s，期望 %s", got, want)
	}
}

// ~ 开头的路径要展开成主目录。
func TestResolveExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("取不到主目录")
	}
	t.Run("~/", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, "")
		got := ResolveAllowlistPath("~/.cua/my.toml")
		want := filepath.Join(home, ".cua", "my.toml")
		if got != want {
			t.Errorf("得到 %s，期望 %s", got, want)
		}
	})
	t.Run("裸 ~", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, "")
		if got := ResolveAllowlistPath("~"); got != home {
			t.Errorf("得到 %s，期望 %s", got, home)
		}
	})
	t.Run("环境变量里的 ~ 也展开", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, "~/env.toml")
		want := filepath.Join(home, "env.toml")
		if got := ResolveAllowlistPath(""); got != want {
			t.Errorf("得到 %s，期望 %s", got, want)
		}
	})
	t.Run("绝对路径不受影响", func(t *testing.T) {
		t.Setenv(AllowlistPathEnv, "")
		p := filepath.Join(t.TempDir(), "a.toml")
		if got := ResolveAllowlistPath(p); got != p {
			t.Errorf("得到 %s，期望原样 %s", got, p)
		}
	})
}
