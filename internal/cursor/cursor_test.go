//go:build windows

package cursor

import "testing"

// LooksHijacked 是判断「光标是否被接管」的唯一依据，
// 它必须对几种异常形态都给出正确判断。
func TestLooksHijacked(t *testing.T) {
	cases := []struct {
		name string
		st   State
		want bool
	}{
		{"正常的 32x32 箭头", State{Visible: true, Width: 32, Height: 32}, false},
		{"大光标", State{Visible: true, Width: 48, Height: 48}, false},
		{"不可见标志", State{Visible: false, Width: 32, Height: 32}, true},
		{"空白位图 0x0", State{Visible: true, Width: 0, Height: 0}, true},
		{"1x1 占位", State{Visible: true, Width: 1, Height: 1}, true},
		{"不可见且空白", State{Visible: false, Width: 0, Height: 0}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.st.LooksHijacked(); got != tc.want {
				t.Errorf("LooksHijacked() = %v，期望 %v（%v）", got, tc.want, tc.st)
			}
		})
	}
}

// 探测应能读到一个正常的光标（测试机上光标是好的）。
func TestProbeReturnsSaneState(t *testing.T) {
	st := Probe()
	if st.Width == 0 || st.Height == 0 {
		t.Skipf("本环境读不到位图尺寸，实际: %v", st)
	}
	t.Logf("当前光标: %v", st)
	if st.Width <= 1 && st.Height <= 1 {
		t.Logf("注意：光标当前状态异常: %v", st)
	}
}

// 注册表读取应能拿到光标方案里的文件路径。
func TestSchemePathsReadable(t *testing.T) {
	paths, err := schemePaths()
	if err != nil {
		t.Skipf("读不到光标方案（可能为非交互会话）: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("光标方案为空")
	}
	if _, ok := paths["Arrow"]; !ok {
		t.Errorf("方案里没有 Arrow: %v", paths)
	}
	t.Logf("读到 %d 项光标", len(paths))
}
