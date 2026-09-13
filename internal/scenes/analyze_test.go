package scenes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longcut/internal/appdirs"
	"longcut/internal/ffmpeg"
)

// findFFmpeg mirrors the app's lookup so the test runs wherever the app runs.
func findFFmpeg(t *testing.T) ffmpeg.Bins {
	t.Helper()
	binDir := ""
	if d, err := appdirs.Resolve(); err == nil {
		binDir = d.Bin
	}
	bins, err := ffmpeg.Find(binDir)
	if err != nil {
		t.Skip("ffmpeg not available:", err)
	}
	return bins
}

// makeTestVideo renders 30 s: three 10 s scenes with distinct colours and a
// moving box, so both hard cuts and motion exist. Encoded with a 1 s GOP so
// keyframe sampling gives ~1 sample per second.
func makeTestVideo(t *testing.T, bins ffmpeg.Bins) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "scenes.mp4")
	// Scene 1: testsrc2 (animated), scene 2: static dark colour, scene 3:
	// testsrc (moving bar) — distinct histograms and different motion levels.
	filter := "" +
		"testsrc2=s=320x180:r=30:d=10[a];" +
		"color=c=0x202830:s=320x180:r=30:d=10[b];" +
		"testsrc=s=320x180:r=30:d=10[c];" +
		"[a][b][c]concat=n=3:v=1:a=0,format=yuv420p"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"-f", "lavfi", "-i", filter, "-c:v", "libx264", "-preset", "veryfast", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-t", "30", out}
	if err := bins.Run(ctx, args, nil); err != nil {
		t.Fatalf("render test video: %v", err)
	}
	return out
}

func TestAnalyzeAndSegmentRealVideo(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bins := findFFmpeg(t)
	video := makeTestVideo(t, bins)
	ctx := context.Background()
	info, err := bins.Probe(ctx, video)
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration < 29 || info.Duration > 31 {
		t.Fatalf("unexpected duration %v", info.Duration)
	}

	for _, mode := range []SampleMode{ModeKeyframes, ModeFPS} {
		t.Run(string(mode), func(t *testing.T) {
			a, err := Analyze(ctx, bins, video, info.Duration, AnalyzeOptions{Mode: mode, FPS: 2}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Frames) < 20 {
				t.Fatalf("only %d frames sampled", len(a.Frames))
			}
			// Timestamps must be monotonic and span the file.
			for i := 1; i < len(a.Frames); i++ {
				if a.Frames[i].T <= a.Frames[i-1].T {
					t.Fatalf("non-monotonic timestamps at %d: %v <= %v", i, a.Frames[i].T, a.Frames[i-1].T)
				}
			}
			if last := a.Frames[len(a.Frames)-1].T; last < 27 {
				t.Errorf("last sample at %v, expected near 29", last)
			}
			cuts := DetectCuts(a, DefaultParams())
			if len(cuts) != 2 {
				t.Fatalf("want 2 cuts at ~10s and ~20s, got %+v", cuts)
			}
			tol := a.MeanSpacing() + 0.05
			for i, want := range []float64{10, 20} {
				if d := cuts[i].T - want; d < -tol || d > tol {
					t.Errorf("cut %d at %.2f, want %.0f±%.2f", i, cuts[i].T, want, tol)
				}
			}
			segs := Segmentize(a, DefaultParams())
			if len(segs) != 3 {
				t.Errorf("want 3 segments, got %d: %+v", len(segs), segs)
			}
			if len(segs) == 3 {
				if segs[0].MotionAvg <= 0.001 || segs[2].MotionAvg <= 0.001 {
					t.Errorf("animated scenes should have motion: %+v %+v", segs[0], segs[2])
				}
				if segs[1].MotionAvg > 0.001 {
					t.Errorf("static scene should have ~zero motion: %+v", segs[1])
				}
			}
		})
	}
}

func TestAnalyzeMissingFile(t *testing.T) {
	bins := findFFmpeg(t)
	_, err := Analyze(context.Background(), bins, filepath.Join(os.TempDir(), "definitely-missing.mp4"), 10, AnalyzeOptions{}, nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
