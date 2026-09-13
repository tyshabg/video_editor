// Package frames extracts keyframes from segments and compresses them for the
// vision model (max 1280x720, JPEG quality ~70).
package frames

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"

	"longcut/internal/ffmpeg"
)

// Options controls extraction.
type Options struct {
	MaxWidth  int // default 1280
	MaxHeight int // default 720
	Quality   int // JPEG quality, default 70
}

func (o *Options) defaults() {
	if o.MaxWidth <= 0 {
		o.MaxWidth = 1280
	}
	if o.MaxHeight <= 0 {
		o.MaxHeight = 720
	}
	if o.Quality <= 0 {
		o.Quality = 70
	}
}

// Frame is one extracted keyframe.
type Frame struct {
	T      float64 `json:"t"`
	Path   string  `json:"path"`
	SHA256 string  `json:"sha256"`
	Width  int     `json:"w"`
	Height int     `json:"h"`
	Bytes  int     `json:"bytes"`
}

// CountFor returns how many keyframes to sample for a segment length.
func CountFor(durationSec float64) int {
	switch {
	case durationSec < 12:
		return 3
	case durationSec < 35:
		return 4
	default:
		return 5
	}
}

// Times returns evenly spaced sample times strictly inside [start, end],
// avoiding the very edges where cut transitions or fades live.
func Times(start, end float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	d := end - start
	ts := make([]float64, n)
	for i := 0; i < n; i++ {
		ts[i] = start + d*(float64(i)+0.5)/float64(n)
	}
	return ts
}

// Extract grabs one frame at each time, scales it to fit the max box and
// writes it as JPEG into outDir with the given name prefix. Existing files
// with the expected name are reused (extraction is idempotent).
func Extract(ctx context.Context, bins ffmpeg.Bins, video string, times []float64, outDir, prefix string, opts Options) ([]Frame, error) {
	opts.defaults()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	out := make([]Frame, 0, len(times))
	for i, t := range times {
		name := fmt.Sprintf("%s_%02d.jpg", prefix, i)
		path := filepath.Join(outDir, name)
		fr, err := loadExisting(path, t)
		if err == nil {
			out = append(out, fr)
			continue
		}
		data, w, h, err := grab(ctx, bins, video, t, opts)
		if err != nil {
			return nil, fmt.Errorf("frame at %.2fs: %w", t, err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out = append(out, Frame{T: t, Path: path, SHA256: hex.EncodeToString(sum[:]), Width: w, Height: h, Bytes: len(data)})
	}
	return out, nil
}

func loadExisting(path string, t float64) (Frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Frame{}, err
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Frame{}, err
	}
	sum := sha256.Sum256(data)
	return Frame{T: t, Path: path, SHA256: hex.EncodeToString(sum[:]), Width: cfg.Width, Height: cfg.Height, Bytes: len(data)}, nil
}

// grab decodes a single frame via ffmpeg (input seeking, so it is fast even
// hours into a file), scales it with ffmpeg to the bounding box, and
// re-encodes it in Go at the exact JPEG quality requested.
func grab(ctx context.Context, bins ffmpeg.Bins, video string, t float64, opts Options) ([]byte, int, int, error) {
	scale := fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease:flags=bicubic", opts.MaxWidth, opts.MaxHeight)
	args := []string{
		"-ss", fmt.Sprintf("%.3f", t),
		"-i", video,
		"-frames:v", "1",
		"-an", "-sn",
		"-vf", scale,
		"-f", "image2pipe", "-vcodec", "png", "pipe:1",
	}
	raw, err := bins.Output(ctx, args)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(raw) == 0 {
		return nil, 0, 0, fmt.Errorf("empty frame (past end of file?)")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode frame: %w", err)
	}
	b := img.Bounds()
	// ffmpeg already scaled; ensure an RGBA canvas so the JPEG encoder is fast.
	rgba, ok := img.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(b)
		draw.Draw(rgba, b, img, b.Min, draw.Src)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: opts.Quality}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), b.Dx(), b.Dy(), nil
}

// ImageTokens estimates Claude vision input tokens for an image of w x h.
func ImageTokens(w, h int) int {
	return (w*h + 749) / 750
}
