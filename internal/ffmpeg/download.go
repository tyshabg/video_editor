package ffmpeg

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Download fetches a static ffmpeg + ffprobe build for this platform into
// binDir. Windows: BtbN GPL build (zip). macOS: evermeet.cx builds (one zip per
// binary). Linux users are expected to install ffmpeg from their distro.
func Download(ctx context.Context, binDir string, progress func(done, total int64)) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		url := "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip"
		tmp, err := fetch(ctx, url, binDir, progress)
		if err != nil {
			return err
		}
		defer os.Remove(tmp)
		return extract(tmp, binDir, map[string]string{"ffmpeg.exe": "ffmpeg.exe", "ffprobe.exe": "ffprobe.exe"})
	case "darwin":
		for _, name := range []string{"ffmpeg", "ffprobe"} {
			url := fmt.Sprintf("https://evermeet.cx/ffmpeg/getrelease/%s/zip", name)
			tmp, err := fetch(ctx, url, binDir, progress)
			if err != nil {
				return err
			}
			err = extract(tmp, binDir, map[string]string{name: name})
			os.Remove(tmp)
			if err != nil {
				return err
			}
			os.Chmod(filepath.Join(binDir, name), 0o755)
		}
		return nil
	default:
		return errors.New("automatic download is only available on Windows and macOS; install ffmpeg with your package manager")
	}
}

func fetch(ctx context.Context, url, dir string, progress func(done, total int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.CreateTemp(dir, "ffmpeg-*.zip")
	if err != nil {
		return "", err
	}
	var done int64
	buf := make([]byte, 256<<10)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(f.Name())
				return "", werr
			}
			done += int64(n)
			if progress != nil && time.Since(last) > 200*time.Millisecond {
				progress(done, resp.ContentLength)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(f.Name())
			return "", rerr
		}
	}
	if progress != nil {
		progress(done, resp.ContentLength)
	}
	name := f.Name()
	return name, f.Close()
}

// extract pulls the wanted basenames out of the zip regardless of folder.
func extract(zipPath, dir string, want map[string]string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := 0
	for _, f := range zr.File {
		base := filepath.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		dst, ok := want[base]
		if !ok || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dir, dst), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
		found++
	}
	if found < len(want) {
		return fmt.Errorf("archive did not contain all of %v", want)
	}
	return nil
}
