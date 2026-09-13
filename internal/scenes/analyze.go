// Package scenes performs scene-change and motion analysis on video files and
// splits them into segments suitable for LLM description.
//
// The analysis runs ffmpeg once per file, decoding either only keyframes
// (fast, GPU-accelerated, typically 0.5-2 s apart in game recordings) or a
// fixed sample rate, and streams tiny grayscale frames into Go where per-frame
// motion and histogram distances are computed. No OpenCV dependency.
package scenes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"longcut/internal/ffmpeg"
)

// SampleMode selects how frames are sampled from the source.
type SampleMode string

const (
	// ModeKeyframes decodes only I-frames. Very fast; spacing depends on the
	// recorder's GOP size.
	ModeKeyframes SampleMode = "keyframes"
	// ModeFPS decodes everything and resamples at a fixed rate.
	ModeFPS SampleMode = "fps"
)

// FrameStat is one sampled frame's measurements.
type FrameStat struct {
	T        float64 `json:"t"` // presentation time, seconds
	Motion   float64 `json:"m"` // mean abs pixel diff vs previous sample, 0..1
	HistDist float64 `json:"h"` // histogram L1/2 distance vs previous sample, 0..1
	SlowDist float64 `json:"s"` // histogram distance vs sample slowLag back, 0..1
	Luma     float64 `json:"l"` // mean brightness, 0..1
}

// Analysis is the per-file result, persisted so segmentation can be re-run
// with different parameters without decoding again.
type Analysis struct {
	Mode     SampleMode  `json:"mode"`
	Duration float64     `json:"duration"`
	Frames   []FrameStat `json:"frames"`
}

// MeanSpacing returns the average time between samples.
func (a *Analysis) MeanSpacing() float64 {
	if len(a.Frames) < 2 {
		return a.Duration
	}
	return (a.Frames[len(a.Frames)-1].T - a.Frames[0].T) / float64(len(a.Frames)-1)
}

// AnalyzeOptions tunes the ffmpeg pass.
type AnalyzeOptions struct {
	Mode    SampleMode
	FPS     float64 // for ModeFPS; default 2
	HWAccel bool    // pass -hwaccel auto
	Width   int     // analysis frame size; default 160x90
	Height  int
	// MaxKeyframeSpacing: when Mode is keyframes and the observed spacing exceeds
	// this (seconds), Analyze reruns in fps mode. 0 disables the fallback.
	MaxKeyframeSpacing float64
}

func (o *AnalyzeOptions) defaults() {
	if o.Mode == "" {
		o.Mode = ModeKeyframes
	}
	if o.FPS <= 0 {
		o.FPS = 2
	}
	if o.Width <= 0 || o.Height <= 0 {
		o.Width, o.Height = 160, 90
	}
}

const slowLag = 4
const histBins = 64

// Progress reports analysis progress in seconds of source processed.
type Progress func(doneSec, totalSec float64)

// Analyze samples the file and computes per-frame statistics.
func Analyze(ctx context.Context, bins ffmpeg.Bins, path string, duration float64, opts AnalyzeOptions, progress Progress) (*Analysis, error) {
	opts.defaults()
	a, err := analyzeOnce(ctx, bins, path, duration, opts, progress)
	if err != nil && opts.HWAccel {
		// Hardware decoders occasionally fail on some streams; retry in software.
		sw := opts
		sw.HWAccel = false
		var err2 error
		a, err2 = analyzeOnce(ctx, bins, path, duration, sw, progress)
		if err2 != nil {
			return nil, fmt.Errorf("hwaccel: %v; software: %w", err, err2)
		}
	} else if err != nil {
		return nil, err
	}
	if opts.Mode == ModeKeyframes && opts.MaxKeyframeSpacing > 0 && a.MeanSpacing() > opts.MaxKeyframeSpacing {
		fb := opts
		fb.Mode = ModeFPS
		return analyzeOnce(ctx, bins, path, duration, fb, progress)
	}
	return a, nil
}

var showinfoRe = regexp.MustCompile(`\bn:\s*(\d+)\b.*?\bpts_time:\s*([0-9.eE+-]+|NOPTS)`)

func analyzeOnce(ctx context.Context, bins ffmpeg.Bins, path string, duration float64, opts AnalyzeOptions, progress Progress) (*Analysis, error) {
	var args []string
	if opts.HWAccel {
		args = append(args, "-hwaccel", "auto")
	}
	vf := fmt.Sprintf("scale=%d:%d:flags=area,format=gray,showinfo", opts.Width, opts.Height)
	switch opts.Mode {
	case ModeKeyframes:
		args = append(args, "-skip_frame", "nokey", "-i", path, "-an", "-sn", "-vf", vf, "-fps_mode", "passthrough")
	case ModeFPS:
		vf = fmt.Sprintf("fps=%g,", opts.FPS) + vf
		args = append(args, "-i", path, "-an", "-sn", "-vf", vf)
	default:
		return nil, fmt.Errorf("unknown sample mode %q", opts.Mode)
	}
	args = append(args, "-f", "rawvideo", "-pix_fmt", "gray", "pipe:1")

	var mu sync.Mutex
	pts := map[int]float64{}
	onStderr := func(line string) {
		if !strings.Contains(line, "pts_time") {
			return
		}
		m := showinfoRe.FindStringSubmatch(line)
		if m == nil || m[2] == "NOPTS" {
			return
		}
		n, err1 := strconv.Atoi(m[1])
		t, err2 := strconv.ParseFloat(m[2], 64)
		if err1 == nil && err2 == nil {
			mu.Lock()
			pts[n] = t
			mu.Unlock()
		}
	}

	stdout, wait, err := bins.Stream(ctx, args, onStderr)
	if err != nil {
		return nil, err
	}

	frameLen := opts.Width * opts.Height
	cur := make([]byte, frameLen)
	var prev []byte
	var prevHist []float64
	histRing := make([][]float64, 0, slowLag+1)
	var frames []FrameStat
	nextReport := 0.0

	for {
		_, err := io.ReadFull(stdout, cur)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			_ = wait()
			return nil, err
		}
		fs := FrameStat{}
		hist := histogram(cur)
		fs.Luma = meanLuma(cur)
		if prev != nil {
			fs.Motion = meanAbsDiff(cur, prev)
			fs.HistDist = histDistance(hist, prevHist)
		}
		if len(histRing) == slowLag {
			fs.SlowDist = histDistance(hist, histRing[0])
			histRing = append(histRing[1:], hist)
		} else {
			histRing = append(histRing, hist)
		}
		frames = append(frames, fs)
		if prev == nil {
			prev = make([]byte, frameLen)
		}
		copy(prev, cur)
		prevHist = hist

		if progress != nil && len(frames)%16 == 0 {
			mu.Lock()
			t, ok := pts[len(frames)-1]
			mu.Unlock()
			if ok && t >= nextReport {
				progress(t, duration)
				nextReport = t + 5
			}
		}
	}
	if err := wait(); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames decoded from %s", path)
	}

	// Attach timestamps; interpolate any that showinfo did not report.
	mu.Lock()
	missing := 0
	for i := range frames {
		if t, ok := pts[i]; ok {
			frames[i].T = t
		} else {
			frames[i].T = math.NaN()
			missing++
		}
	}
	mu.Unlock()
	if missing == len(frames) {
		// showinfo unavailable: assume uniform spacing.
		step := duration / float64(len(frames))
		for i := range frames {
			frames[i].T = float64(i) * step
		}
	} else if missing > 0 {
		fillNaN(frames, duration)
	}
	if progress != nil {
		progress(duration, duration)
	}
	return &Analysis{Mode: opts.Mode, Duration: duration, Frames: frames}, nil
}

func fillNaN(frames []FrameStat, duration float64) {
	n := len(frames)
	for i := 0; i < n; i++ {
		if !math.IsNaN(frames[i].T) {
			continue
		}
		// find previous and next known
		pi, ni := i-1, i+1
		for pi >= 0 && math.IsNaN(frames[pi].T) {
			pi--
		}
		for ni < n && math.IsNaN(frames[ni].T) {
			ni++
		}
		var pt, nt float64
		var pIdx, nIdx int
		if pi >= 0 {
			pt, pIdx = frames[pi].T, pi
		} else {
			pt, pIdx = 0, -1
		}
		if ni < n {
			nt, nIdx = frames[ni].T, ni
		} else {
			nt, nIdx = duration, n
		}
		frames[i].T = pt + (nt-pt)*float64(i-pIdx)/float64(nIdx-pIdx)
	}
}

func meanAbsDiff(a, b []byte) float64 {
	var sum int64
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += int64(d)
	}
	return float64(sum) / float64(len(a)) / 255
}

func meanLuma(a []byte) float64 {
	var sum int64
	for _, v := range a {
		sum += int64(v)
	}
	return float64(sum) / float64(len(a)) / 255
}

func histogram(a []byte) []float64 {
	h := make([]float64, histBins)
	for _, v := range a {
		h[int(v)*histBins/256]++
	}
	inv := 1 / float64(len(a))
	for i := range h {
		h[i] *= inv
	}
	return h
}

// histDistance is half the L1 distance between two normalized histograms (0..1).
func histDistance(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += math.Abs(a[i] - b[i])
	}
	return s / 2
}
