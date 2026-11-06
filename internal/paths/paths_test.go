package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRelativeUsesExeDir(t *testing.T) {
	want := filepath.Join(BaseDir(), "logs")
	if got := Resolve("logs"); got != want {
		t.Errorf("Resolve(\"logs\") = %q, 期望 %q", got, want)
	}
	if !filepath.IsAbs(Resolve("logs")) {
		t.Error("相对路径解析后必须是绝对路径")
	}
}

func TestResolveAbsoluteUnchanged(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x.json")
	if got := Resolve(abs); got != filepath.Clean(abs) {
		t.Errorf("绝对路径被改写: %q -> %q", abs, got)
	}
}

func TestResolveEmpty(t *testing.T) {
	if got := Resolve(""); got != "" {
		t.Errorf("Resolve(\"\") = %q, 期望空", got)
	}
}

func TestBaseDirStableAndExists(t *testing.T) {
	a, b := BaseDir(), BaseDir()
	if a != b {
		t.Errorf("BaseDir 不稳定: %q vs %q", a, b)
	}
	fi, err := os.Stat(a)
	if err != nil || !fi.IsDir() {
		t.Errorf("BaseDir 不是有效目录: %q (%v)", a, err)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	want := filepath.Join(BaseDir(), "crontab.json")
	if got := DefaultConfigPath(); got != want {
		t.Errorf("DefaultConfigPath() = %q, 期望 %q", got, want)
	}
}
