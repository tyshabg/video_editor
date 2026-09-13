// Package ffmpeg locates and drives the ffmpeg/ffprobe binaries.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Bins holds resolved binary paths.
type Bins struct {
	FFmpeg  string
	FFprobe string
}

var ErrNotFound = errors.New("ffmpeg not found: set LONGCUT_FFMPEG, put ffmpeg into the app bin dir, or install it into PATH")

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

// Find locates ffmpeg and ffprobe. Search order: LONGCUT_FFMPEG (dir or ffmpeg
// binary), binDir, PATH.
func Find(binDir string) (Bins, error) {
	var candidates []string
	if env := os.Getenv("LONGCUT_FFMPEG"); env != "" {
		if st, err := os.Stat(env); err == nil && st.IsDir() {
			candidates = append(candidates, env)
		} else {
			candidates = append(candidates, filepath.Dir(env))
		}
	}
	if binDir != "" {
		candidates = append(candidates, binDir)
	}
	for _, dir := range candidates {
		ff := filepath.Join(dir, exeName("ffmpeg"))
		fp := filepath.Join(dir, exeName("ffprobe"))
		if fileExists(ff) && fileExists(fp) {
			return Bins{FFmpeg: ff, FFprobe: fp}, nil
		}
	}
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 == nil && err2 == nil {
		return Bins{FFmpeg: ff, FFprobe: fp}, nil
	}
	return Bins{}, ErrNotFound
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Version returns the first line of the ffmpeg version banner.
func (b Bins) Version(ctx context.Context) (string, error) {
	out, err := command(ctx, b.FFmpeg, "-version").Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line), nil
}

// Info describes a media file.
type Info struct {
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	FPS      float64 `json:"fps"`
	Codec    string  `json:"codec"`
	HasAudio bool    `json:"has_audio"`
	Size     int64   `json:"size"`
	// CreationTime is the container's creation_time tag when present (UTC).
	CreationTime time.Time `json:"creation_time"`
}

// Probe runs ffprobe and extracts the primary video stream info.
func (b Bins) Probe(ctx context.Context, path string) (Info, error) {
	args := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path}
	out, err := command(ctx, b.FFprobe, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Info{}, fmt.Errorf("ffprobe %s: %s", filepath.Base(path), strings.TrimSpace(string(ee.Stderr)))
		}
		return Info{}, err
	}
	var raw struct {
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			RFrameRate string `json:"r_frame_rate"`
			AvgRate    string `json:"avg_frame_rate"`
			Duration   string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
			Tags     struct {
				CreationTime string `json:"creation_time"`
			} `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return Info{}, fmt.Errorf("parse ffprobe output: %w", err)
	}
	info := Info{}
	info.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	info.Size, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	if ct := raw.Format.Tags.CreationTime; ct != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, ct); err == nil {
				info.CreationTime = t
				break
			}
		}
	}
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if info.Width != 0 {
				continue
			}
			info.Width, info.Height, info.Codec = s.Width, s.Height, s.CodecName
			info.FPS = parseRate(s.AvgRate)
			if info.FPS == 0 {
				info.FPS = parseRate(s.RFrameRate)
			}
			if info.Duration == 0 {
				info.Duration, _ = strconv.ParseFloat(s.Duration, 64)
			}
		case "audio":
			info.HasAudio = true
		}
	}
	if info.Width == 0 {
		return Info{}, fmt.Errorf("%s: no video stream", filepath.Base(path))
	}
	return info, nil
}

func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	n, _ := strconv.ParseFloat(num, 64)
	d, _ := strconv.ParseFloat(den, 64)
	if d == 0 {
		return 0
	}
	return n / d
}

// Run executes ffmpeg with the given args (without the binary name), reporting
// progress through onProgress (seconds of output produced) when non-nil.
// It appends -progress pipe:1, so callers must not write media to stdout.
func (b Bins) Run(ctx context.Context, args []string, onProgress func(outSec float64)) error {
	full := append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, args...)
	full = append(full, "-progress", "pipe:1", "-nostats")
	cmd := command(ctx, b.FFmpeg, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if onProgress == nil {
			continue
		}
		if v, ok := strings.CutPrefix(line, "out_time_us="); ok {
			if us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && us >= 0 {
				onProgress(float64(us) / 1e6)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg: %s", lastLines(msg, 5))
	}
	return nil
}

// Output runs ffmpeg and returns raw stdout (used for single-frame extraction).
func (b Bins) Output(ctx context.Context, args []string) ([]byte, error) {
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, args...)
	cmd := command(ctx, b.FFmpeg, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: %s", lastLines(strings.TrimSpace(stderr.String()), 5))
	}
	return out, nil
}

// Stream starts ffmpeg and returns its stdout for streaming consumption plus a
// wait function. stderr lines are passed to onStderr when non-nil (used to
// parse showinfo timestamps); the last non-showinfo lines are reported by wait
// on failure.
func (b Bins) Stream(ctx context.Context, args []string, onStderr func(line string)) (io.ReadCloser, func() error, error) {
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "info", "-nostats"}, args...)
	cmd := command(ctx, b.FFmpeg, full...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	var tail []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if onStderr != nil {
				onStderr(line)
			}
			if !strings.Contains(line, "showinfo") {
				tail = append(tail, line)
				if len(tail) > 8 {
					tail = tail[1:]
				}
			}
		}
	}()
	wait := func() error {
		<-done
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("ffmpeg: %s", strings.Join(tail, "\n"))
		}
		return nil
	}
	return stdout, wait, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
