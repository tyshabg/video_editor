package appdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAtCreatesLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	d, err := ResolveAt(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d.Bin, d.Frames, d.Proxies, d.Exports, d.Logs} {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			t.Errorf("%s not created", p)
		}
	}
	if d.HasData() {
		t.Error("fresh dir should have no data")
	}
}

func TestEnvOverrideWins(t *testing.T) {
	root := filepath.Join(t.TempDir(), "env")
	t.Setenv("LONGCUT_DATA_DIR", root)
	d, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if d.Root != filepath.Clean(root) || d.Source != "env" {
		t.Fatalf("got %+v", d)
	}
}

func TestMoveAcrossDirs(t *testing.T) {
	base := t.TempDir()
	src, _ := ResolveAt(filepath.Join(base, "old"))
	dst, _ := ResolveAt(filepath.Join(base, "new"))
	os.WriteFile(src.DBPath, []byte("db"), 0o644)
	os.MkdirAll(filepath.Join(src.Frames, "k1"), 0o755)
	os.WriteFile(filepath.Join(src.Frames, "k1", "a.jpg"), make([]byte, 1000), 0o644)
	os.WriteFile(filepath.Join(src.Bin, "ffmpeg"), []byte("x"), 0o755)

	var last int64
	if err := Move(src, dst, func(done, total int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if !dst.HasData() {
		t.Error("db not moved")
	}
	if _, err := os.Stat(filepath.Join(dst.Frames, "k1", "a.jpg")); err != nil {
		t.Error("frame cache not moved")
	}
	if _, err := os.Stat(filepath.Join(dst.Bin, "ffmpeg")); err != nil {
		t.Error("bin not moved")
	}
	if _, err := os.Stat(src.DBPath); err == nil {
		t.Error("old db still present")
	}
	if last <= 0 {
		t.Error("no progress reported")
	}
}

func TestMoveRejectsNesting(t *testing.T) {
	base := t.TempDir()
	src, _ := ResolveAt(filepath.Join(base, "old"))
	dst, _ := ResolveAt(filepath.Join(base, "old", "inner"))
	if err := Move(src, dst, nil); err == nil {
		t.Fatal("nested move should fail")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := SaveConfig(Config{DataDir: `K:\somewhere`}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig()
	if err != nil || c.DataDir != `K:\somewhere` {
		t.Fatalf("got %+v %v", c, err)
	}
}
