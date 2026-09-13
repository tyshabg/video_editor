// Package proxy renders small preview clips for segments so the UI can play
// them inside the WebView without touching the multi-gigabyte originals.
package proxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"longcut/internal/ffmpeg"
)

// Options for proxy rendering.
type Options struct {
	Height int // default 480
	CRF    int // default 26
}

// RelPath returns the cache-relative path of a segment's proxy.
func RelPath(fileKey string, start, end float64) string {
	return filepath.Join(fileKey, fmt.Sprintf("%09d-%09d.mp4", int64(start*1000), int64(end*1000)))
}

// Ensure renders the proxy if it is missing and returns its relative path.
func Ensure(ctx context.Context, bins ffmpeg.Bins, root, video, fileKey string, start, end float64, opts Options) (string, error) {
	if opts.Height <= 0 {
		opts.Height = 480
	}
	if opts.CRF <= 0 {
		opts.CRF = 26
	}
	rel := RelPath(fileKey, start, end)
	abs := filepath.Join(root, rel)
	if st, err := os.Stat(abs); err == nil && st.Size() > 0 {
		return rel, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	tmp := abs + ".part.mp4"
	build := func(hw bool) []string {
		var args []string
		if hw {
			args = append(args, "-hwaccel", "auto")
		}
		args = append(args,
			"-ss", fmt.Sprintf("%.3f", start), "-to", fmt.Sprintf("%.3f", end), "-i", video,
			"-vf", fmt.Sprintf("scale=-2:%d", opts.Height), "-pix_fmt", "yuv420p",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", fmt.Sprint(opts.CRF), "-g", "60",
			"-c:a", "aac", "-b:a", "96k", "-ac", "2",
			"-movflags", "+faststart", "-f", "mp4", tmp)
		return args
	}
	err := bins.Run(ctx, build(true), nil)
	if err != nil && ctx.Err() == nil {
		err = bins.Run(ctx, build(false), nil)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, abs); err != nil {
		return "", err
	}
	return rel, nil
}
