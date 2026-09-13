// Package appdirs resolves per-user application directories for Longcut.
//
// The data directory (database, caches, downloaded ffmpeg) can be large, so
// the user may relocate it. Resolution order: LONGCUT_DATA_DIR environment
// variable, then the path stored in a small config file at the platform
// default location, then the platform default itself.
package appdirs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const AppName = "Longcut"

// Dirs holds the resolved application directories.
type Dirs struct {
	Root    string // base data dir
	Bin     string // downloaded ffmpeg binaries
	Frames  string // keyframe JPEG cache
	Proxies string // low-res preview clips
	Exports string // default export location
	Logs    string
	DBPath  string
	Source  string // "env" | "config" | "default"
}

// Config is the small persistent settings file kept at the platform default
// location (never relocated, so it can always be found).
type Config struct {
	DataDir string `json:"data_dir,omitempty"`
	// APIKey is stored only when the user asks the app to remember it; the
	// ANTHROPIC_API_KEY environment variable always takes precedence.
	APIKey string `json:"api_key,omitempty"`
}

// Resolve returns the application directories.
func Resolve() (Dirs, error) {
	if root := os.Getenv("LONGCUT_DATA_DIR"); root != "" {
		d, err := ResolveAt(root)
		d.Source = "env"
		return d, err
	}
	if cfg, err := LoadConfig(); err == nil && cfg.DataDir != "" {
		d, err := ResolveAt(cfg.DataDir)
		if err == nil {
			d.Source = "config"
			return d, nil
		}
		// Configured location unusable (drive unplugged?): fall through to default.
	}
	root, err := platformDefault()
	if err != nil {
		return Dirs{}, err
	}
	d, err := ResolveAt(root)
	d.Source = "default"
	return d, err
}

// ResolveAt builds the directory layout under an explicit root and creates it.
func ResolveAt(root string) (Dirs, error) {
	root = filepath.Clean(root)
	d := Dirs{
		Root:    root,
		Bin:     filepath.Join(root, "bin"),
		Frames:  filepath.Join(root, "cache", "frames"),
		Proxies: filepath.Join(root, "cache", "proxies"),
		Exports: filepath.Join(root, "exports"),
		Logs:    filepath.Join(root, "logs"),
		DBPath:  filepath.Join(root, "longcut.db"),
		Source:  "explicit",
	}
	for _, p := range []string{d.Root, d.Bin, d.Frames, d.Proxies, d.Exports, d.Logs} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return Dirs{}, err
		}
	}
	return d, nil
}

// EnvOverride reports whether LONGCUT_DATA_DIR is set (it wins over config).
func EnvOverride() bool { return os.Getenv("LONGCUT_DATA_DIR") != "" }

// ConfigPath returns the location of the settings file.
func ConfigPath() (string, error) {
	base, err := platformDefault()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "config.json"), nil
}

// LoadConfig reads the settings file; a missing file yields an empty config.
func LoadConfig() (Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", p, err)
	}
	return c, nil
}

// SaveConfig writes the settings file.
func SaveConfig(c Config) error {
	p, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

// Size walks the data directory and sums file sizes.
func (d Dirs) Size() int64 {
	var total int64
	_ = filepath.WalkDir(d.Root, func(_ string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// HasData reports whether a data directory already contains a database.
func (d Dirs) HasData() bool {
	_, err := os.Stat(d.DBPath)
	return err == nil
}

// Move copies everything Longcut owns from src to dst and removes the
// originals. Works across volumes. The database must be closed first. Files
// that already exist at dst are overwritten.
func Move(src, dst Dirs, progress func(done, total int64)) error {
	if sameDir(src.Root, dst.Root) {
		return nil
	}
	if within(dst.Root, src.Root) || within(src.Root, dst.Root) {
		return fmt.Errorf("new folder must not be inside the old one (or vice versa)")
	}
	total := src.Size()
	var done int64
	items := []string{"longcut.db", "longcut.db-wal", "longcut.db-shm", "bin", "cache", "exports", "logs"}
	for _, name := range items {
		from := filepath.Join(src.Root, name)
		to := filepath.Join(dst.Root, name)
		st, err := os.Stat(from)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		// Fast path: same volume rename.
		if os.Rename(from, to) == nil {
			done += dirSize(to)
			if progress != nil {
				progress(done, total)
			}
			continue
		}
		if err := copyTree(from, to, st, func(n int64) {
			done += n
			if progress != nil {
				progress(done, total)
			}
		}); err != nil {
			return err
		}
		if err := os.RemoveAll(from); err != nil {
			return err
		}
	}
	// Remove the old root if it is now empty.
	if entries, err := os.ReadDir(src.Root); err == nil && len(entries) == 0 {
		_ = os.Remove(src.Root)
	}
	return nil
}

func copyTree(from, to string, st os.FileInfo, add func(int64)) error {
	if !st.IsDir() {
		return copyFile(from, to, add)
	}
	return filepath.WalkDir(from, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target, add)
	})
}

func copyFile(from, to string, add func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if st, err := os.Stat(from); err == nil {
		_ = os.Chmod(tmp, st.Mode())
	}
	if err := os.Rename(tmp, to); err != nil {
		return err
	}
	add(n)
	return nil
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if i, err := e.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

func sameDir(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return filepath.Clean(aa) == filepath.Clean(bb) || equalFoldPath(aa, bb)
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func equalFoldPath(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// within reports whether child is inside parent.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !hasDotDotPrefix(rel)
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".." && (len(rel) == 2 || rel[2] == filepath.Separator)
}

func platformDefault() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, AppName), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", AppName), nil
	default:
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, AppName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", AppName), nil
	}
}
