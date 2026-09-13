package scenes

import (
	"math"
	"math/rand"
	"testing"
)

// synth builds an analysis sampled at `fps` with hard cuts at the given times
// and low background noise.
func synth(duration, fps float64, cuts []float64, seed int64) *Analysis {
	rng := rand.New(rand.NewSource(seed))
	n := int(duration * fps)
	a := &Analysis{Mode: ModeFPS, Duration: duration}
	cutSet := map[int]bool{}
	for _, c := range cuts {
		cutSet[int(math.Round(c*fps))] = true
	}
	for i := 0; i < n; i++ {
		f := FrameStat{T: float64(i) / fps, Luma: 0.4}
		if i > 0 {
			f.Motion = 0.02 + rng.Float64()*0.02
			f.HistDist = 0.02 + rng.Float64()*0.03
			f.SlowDist = 0.05 + rng.Float64()*0.05
		}
		if cutSet[i] {
			f.HistDist = 0.7
			f.Motion = 0.5
			f.SlowDist = 0.7
		}
		a.Frames = append(a.Frames, f)
	}
	return a
}

func assertBounds(t *testing.T, segs []Segment, p Params, duration float64) {
	t.Helper()
	if len(segs) == 0 {
		t.Fatal("no segments")
	}
	if segs[0].Start != 0 {
		t.Errorf("first segment starts at %v, want 0", segs[0].Start)
	}
	if math.Abs(segs[len(segs)-1].End-duration) > 1e-9 {
		t.Errorf("last segment ends at %v, want %v", segs[len(segs)-1].End, duration)
	}
	for i, s := range segs {
		if i > 0 && math.Abs(s.Start-segs[i-1].End) > 1e-9 {
			t.Errorf("gap between segment %d and %d: %v vs %v", i-1, i, segs[i-1].End, s.Start)
		}
		d := s.Duration()
		if d > p.MaxLen+1e-9 {
			t.Errorf("segment %d too long: %.2fs [%.1f-%.1f]", i, d, s.Start, s.End)
		}
		if d < p.MinLen-1e-9 {
			t.Errorf("segment %d too short: %.2fs [%.1f-%.1f]", i, d, s.Start, s.End)
		}
	}
}

func hasBoundary(segs []Segment, t, tol float64) bool {
	for _, s := range segs {
		if math.Abs(s.Start-t) <= tol {
			return true
		}
	}
	return false
}

func TestDetectCutsFindsHardCuts(t *testing.T) {
	a := synth(200, 2, []float64{30, 75, 140}, 1)
	cuts := DetectCuts(a, DefaultParams())
	if len(cuts) != 3 {
		t.Fatalf("got %d cuts, want 3: %+v", len(cuts), cuts)
	}
	for i, want := range []float64{30, 75, 140} {
		if math.Abs(cuts[i].T-want) > 1e-9 {
			t.Errorf("cut %d at %v, want %v", i, cuts[i].T, want)
		}
	}
}

func TestDetectCutsSuppressesFlicker(t *testing.T) {
	// High constant histogram noise (e.g. strobing effects) must not produce a
	// cut on every frame.
	a := synth(60, 2, nil, 2)
	for i := range a.Frames {
		if i > 0 {
			a.Frames[i].HistDist = 0.35
			a.Frames[i].SlowDist = 0.3
		}
	}
	cuts := DetectCuts(a, DefaultParams())
	if len(cuts) > 2 {
		t.Fatalf("flicker produced %d cuts", len(cuts))
	}
}

func TestDetectCutsFade(t *testing.T) {
	// A slow fade: per-frame distance stays under the hard threshold but the
	// distance across slowLag frames crosses the slow threshold once.
	a := synth(60, 2, nil, 3)
	for i := 40; i < 46; i++ {
		a.Frames[i].HistDist = 0.15
	}
	for i := 43; i < 50; i++ {
		a.Frames[i].SlowDist = 0.6
	}
	cuts := DetectCuts(a, DefaultParams())
	if len(cuts) != 1 || !cuts[0].Slow {
		t.Fatalf("want exactly one slow cut, got %+v", cuts)
	}
}

func TestSegmentizeMergesShortAndKeepsCuts(t *testing.T) {
	p := DefaultParams()
	a := synth(200, 2, []float64{30, 75, 76.5, 140}, 4)
	segs := Segmentize(a, p)
	assertBounds(t, segs, p, 200)
	for _, want := range []float64{30, 140} {
		if !hasBoundary(segs, want, 1e-9) {
			t.Errorf("boundary at %v lost", want)
		}
	}
	// 75 and 76.5 are 1.5s apart: one of them must survive, not both.
	if hasBoundary(segs, 75, 1e-9) && hasBoundary(segs, 76.5, 1e-9) {
		t.Errorf("short segment 75-76.5 was not merged")
	}
}

func TestSegmentizeSplitsLongStatic(t *testing.T) {
	p := DefaultParams()
	a := synth(300, 2, nil, 5)
	segs := Segmentize(a, p)
	assertBounds(t, segs, p, 300)
	if len(segs) < 5 {
		t.Errorf("300s static footage produced only %d segments", len(segs))
	}
}

func TestSegmentizeSplitsAtMotionTransition(t *testing.T) {
	p := DefaultParams()
	a := synth(100, 2, nil, 6)
	// Calm for 0-43s, action 43-100s. Expect a split near 43.
	for i := range a.Frames {
		if a.Frames[i].T >= 43 {
			a.Frames[i].Motion = 0.25 + a.Frames[i].Motion
		}
	}
	segs := Segmentize(a, p)
	assertBounds(t, segs, p, 100)
	if !hasBoundary(segs, 43, 2.0) {
		t.Errorf("expected split near t=43, got %+v", segs)
	}
}

func TestSegmentizeShortVideo(t *testing.T) {
	p := DefaultParams()
	a := synth(3, 2, nil, 7)
	segs := Segmentize(a, p)
	if len(segs) != 1 || segs[0].Start != 0 || segs[0].End != 3 {
		t.Fatalf("short video should be one segment, got %+v", segs)
	}
}

func TestSegmentizeManyCutsDense(t *testing.T) {
	// Cuts every 2 seconds (e.g. rapid cutscene): everything merges into
	// segments of at least MinLen without exceeding MaxLen.
	p := DefaultParams()
	var cuts []float64
	for t := 2.0; t < 120; t += 2 {
		cuts = append(cuts, t)
	}
	a := synth(120, 2, cuts, 8)
	segs := Segmentize(a, p)
	assertBounds(t, segs, p, 120)
}

func TestFillStats(t *testing.T) {
	a := synth(20, 2, []float64{10}, 9)
	segs := Segmentize(a, DefaultParams())
	for _, s := range segs {
		if s.MotionAvg <= 0 || s.MotionAvg > 0.1 {
			t.Errorf("motion avg %v out of expected range for %+v", s.MotionAvg, s)
		}
		if s.LumaAvg < 0.39 || s.LumaAvg > 0.41 {
			t.Errorf("luma avg %v", s.LumaAvg)
		}
	}
}

func TestFillNaN(t *testing.T) {
	fr := []FrameStat{{T: 0}, {T: math.NaN()}, {T: math.NaN()}, {T: 3}, {T: math.NaN()}}
	fillNaN(fr, 5)
	want := []float64{0, 1, 2, 3, 4}
	for i := range fr {
		if math.Abs(fr[i].T-want[i]) > 1e-9 {
			t.Errorf("frame %d T=%v want %v", i, fr[i].T, want[i])
		}
	}
}
