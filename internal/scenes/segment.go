package scenes

import (
	"math"
	"sort"
)

// Params controls segmentation.
type Params struct {
	MinLen float64 // seconds; shorter segments are merged into a neighbour
	MaxLen float64 // seconds; longer segments are split at motion transitions
	// CutThreshold: histogram distance between consecutive samples that marks a
	// hard cut (0..1). SlowCutThreshold applies to the distance across slowLag
	// samples and catches fades/dissolves.
	CutThreshold     float64
	SlowCutThreshold float64
	// AdaptiveFactor: a cut must also exceed this multiple of the recent
	// baseline distance, which suppresses false cuts in flickery/high-motion
	// footage.
	AdaptiveFactor float64
}

// DefaultParams are tuned for game footage sampled at 0.5-2 fps.
func DefaultParams() Params {
	return Params{
		MinLen:           5,
		MaxLen:           60,
		CutThreshold:     0.30,
		SlowCutThreshold: 0.55,
		AdaptiveFactor:   2.5,
	}
}

// Segment is a proposed clip boundary with activity statistics.
type Segment struct {
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	MotionAvg float64 `json:"motion_avg"`
	MotionMax float64 `json:"motion_max"`
	LumaAvg   float64 `json:"luma_avg"`
	CutScore  float64 `json:"cut_score"` // strength of the boundary at Start (0 = synthetic split)
}

// Duration of the segment in seconds.
func (s Segment) Duration() float64 { return s.End - s.Start }

// Cut is a detected scene boundary.
type Cut struct {
	Index int     // frame index that starts the new scene
	T     float64 // time of that frame
	Score float64
	Slow  bool // detected via the slow (fade) detector
}

// DetectCuts finds scene boundaries in the analysis.
//
// Hard cuts: the per-sample histogram distance must exceed CutThreshold and
// AdaptiveFactor times the rolling median of recent distances (the median is
// immune to isolated spikes, so real cuts do not raise the baseline, while
// sustained flicker does and is suppressed).
//
// Slow cuts (fades/dissolves): the distance across slowLag samples crosses
// SlowCutThreshold. The detector is re-armed only after the slow distance
// falls back, so one fade yields one cut.
func DetectCuts(a *Analysis, p Params) []Cut {
	const baselineWin = 10
	var cuts []Cut
	lastCut := -slowLag - 1
	recent := make([]float64, 0, baselineWin+1)
	slowArmed := true
	for i := 1; i < len(a.Frames); i++ {
		f := a.Frames[i]
		baseline := median(recent)
		isCut := false
		score := f.HistDist
		slow := false
		if f.HistDist >= p.CutThreshold && f.HistDist >= p.AdaptiveFactor*baseline {
			isCut = true
		} else if slowArmed && i-lastCut > slowLag && f.SlowDist >= p.SlowCutThreshold {
			isCut, slow, score = true, true, f.SlowDist
		}
		if f.SlowDist < p.SlowCutThreshold*0.6 {
			slowArmed = true
		}
		if isCut {
			cuts = append(cuts, Cut{Index: i, T: f.T, Score: score, Slow: slow})
			lastCut = i
			slowArmed = false
		}
		recent = append(recent, f.HistDist)
		if len(recent) > baselineWin {
			recent = recent[1:]
		}
	}
	return cuts
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	m := len(c) / 2
	if len(c)%2 == 1 {
		return c[m]
	}
	return (c[m-1] + c[m]) / 2
}

// Segmentize turns an analysis into segments within [MinLen, MaxLen].
func Segmentize(a *Analysis, p Params) []Segment {
	if len(a.Frames) == 0 || a.Duration <= 0 {
		return nil
	}
	cuts := DetectCuts(a, p)

	// Raw boundaries.
	type bound struct {
		t, score float64
	}
	bounds := []bound{{0, 1}}
	for _, c := range cuts {
		if c.T > bounds[len(bounds)-1].t {
			bounds = append(bounds, bound{c.T, c.Score})
		}
	}
	bounds = append(bounds, bound{a.Duration, 1})

	segs := make([]Segment, 0, len(bounds))
	for i := 0; i+1 < len(bounds); i++ {
		segs = append(segs, Segment{Start: bounds[i].t, End: bounds[i+1].t, CutScore: bounds[i].score})
	}

	segs = mergeShort(segs, p)
	segs = splitLong(segs, a, p)
	for i := range segs {
		fillStats(&segs[i], a)
	}
	return segs
}

// mergeShort merges segments shorter than MinLen into the neighbour joined by
// the weaker cut, preferring not to exceed MaxLen.
func mergeShort(segs []Segment, p Params) []Segment {
	changed := true
	for changed && len(segs) > 1 {
		changed = false
		// Merge the shortest first so tiny flickers are absorbed before decisions
		// about moderately short segments are made.
		best := -1
		for i, s := range segs {
			if s.Duration() < p.MinLen && (best < 0 || s.Duration() < segs[best].Duration()) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		i := best
		canPrev := i > 0
		canNext := i+1 < len(segs)
		mergeWithPrev := false
		switch {
		case canPrev && canNext:
			prevLen := segs[i-1].Duration() + segs[i].Duration()
			nextLen := segs[i].Duration() + segs[i+1].Duration()
			prevOK := prevLen <= p.MaxLen
			nextOK := nextLen <= p.MaxLen
			switch {
			case prevOK && nextOK:
				// weaker cut wins: cut at segs[i].Start joins prev, at segs[i+1].Start joins next
				mergeWithPrev = segs[i].CutScore <= segs[i+1].CutScore
			case prevOK:
				mergeWithPrev = true
			case nextOK:
				mergeWithPrev = false
			default:
				mergeWithPrev = segs[i].CutScore <= segs[i+1].CutScore
			}
		case canPrev:
			mergeWithPrev = true
		case canNext:
			mergeWithPrev = false
		default:
			return segs
		}
		if mergeWithPrev {
			segs[i-1].End = segs[i].End
			segs = append(segs[:i], segs[i+1:]...)
		} else {
			segs[i+1].Start = segs[i].Start
			segs[i+1].CutScore = segs[i].CutScore
			segs = append(segs[:i], segs[i+1:]...)
		}
		changed = true
	}
	return segs
}

// splitLong breaks segments longer than MaxLen at motion transitions.
func splitLong(segs []Segment, a *Analysis, p Params) []Segment {
	var out []Segment
	for _, s := range segs {
		if s.Duration() <= p.MaxLen {
			out = append(out, s)
			continue
		}
		points := splitPoints(s, a, p)
		start := s.Start
		score := s.CutScore
		for _, t := range points {
			out = append(out, Segment{Start: start, End: t, CutScore: score})
			start, score = t, 0
		}
		out = append(out, Segment{Start: start, End: s.End, CutScore: score})
	}
	return out
}

// splitPoints picks interior split times for a long segment. It prefers
// moments where smoothed motion changes the most (calm->action or back),
// keeping every piece within [MinLen, MaxLen].
func splitPoints(s Segment, a *Analysis, p Params) []float64 {
	// Collect frames inside the segment.
	lo := sort.Search(len(a.Frames), func(i int) bool { return a.Frames[i].T >= s.Start })
	hi := sort.Search(len(a.Frames), func(i int) bool { return a.Frames[i].T >= s.End })
	n := hi - lo
	dur := s.Duration()
	// Aim below MaxLen so the chosen split points have room to move towards
	// motion transitions without pushing a later piece over the limit.
	pieces := int(math.Ceil(dur / (p.MaxLen * 0.85)))
	if pieces < 2 {
		pieces = 2
	}
	target := dur / float64(pieces)

	if n < 8 {
		return evenSplit(s.Start, dur, pieces)
	}

	// Smoothed motion and its change score.
	const w = 3
	sm := make([]float64, n)
	for i := 0; i < n; i++ {
		var sum float64
		cnt := 0
		for j := i - w; j <= i+w; j++ {
			if j >= 0 && j < n {
				sum += a.Frames[lo+j].Motion
				cnt++
			}
		}
		sm[i] = sum / float64(cnt)
	}
	change := make([]float64, n)
	for i := w; i < n-w; i++ {
		change[i] = math.Abs(sm[i+w] - sm[i-w])
	}

	var points []float64
	cur := s.Start
	for k := 1; k < pieces; k++ {
		ideal := cur + target
		remaining := float64(pieces - k)
		// Hard window: keeps this piece and every remaining piece within bounds.
		hardLo := math.Max(cur+p.MinLen, s.End-remaining*p.MaxLen)
		hardHi := math.Min(cur+p.MaxLen, s.End-remaining*p.MinLen)
		if hardHi < hardLo {
			return evenSplit(s.Start, dur, pieces)
		}
		// Soft window: stay reasonably close to the ideal position.
		softLo := math.Max(hardLo, ideal-target*0.4)
		softHi := math.Min(hardHi, ideal+target*0.4)
		if softHi < softLo {
			softLo, softHi = hardLo, hardHi
		}
		bestT := math.Min(math.Max(ideal, softLo), softHi)
		bestScore := -1.0
		for i := 0; i < n; i++ {
			t := a.Frames[lo+i].T
			if t < softLo || t > softHi {
				continue
			}
			// Mild preference for the ideal position so equal-scored candidates
			// yield balanced pieces.
			sc := change[i] - 0.02*math.Abs(t-ideal)/target
			if sc > bestScore {
				bestScore, bestT = sc, t
			}
		}
		points = append(points, bestT)
		cur = bestT
	}
	return points
}

func evenSplit(start, dur float64, pieces int) []float64 {
	var pts []float64
	for k := 1; k < pieces; k++ {
		pts = append(pts, start+dur*float64(k)/float64(pieces))
	}
	return pts
}

func fillStats(s *Segment, a *Analysis) {
	lo := sort.Search(len(a.Frames), func(i int) bool { return a.Frames[i].T >= s.Start })
	hi := sort.Search(len(a.Frames), func(i int) bool { return a.Frames[i].T >= s.End })
	if hi <= lo {
		if lo > 0 {
			lo--
		}
		hi = lo + 1
	}
	var sumM, sumL, maxM float64
	cnt := 0
	for i := lo; i < hi && i < len(a.Frames); i++ {
		f := a.Frames[i]
		if i == lo {
			// The first frame's motion belongs to the cut itself, skip it when
			// there are other frames.
			if hi-lo > 1 {
				sumL += f.Luma
				cnt++
				continue
			}
		}
		sumM += f.Motion
		sumL += f.Luma
		maxM = math.Max(maxM, f.Motion)
		cnt++
	}
	if cnt > 0 {
		s.LumaAvg = sumL / float64(cnt)
	}
	if m := hi - lo - 1; m > 0 {
		s.MotionAvg = sumM / float64(m)
	} else if cnt > 0 {
		s.MotionAvg = sumM / float64(cnt)
	}
	s.MotionMax = maxM
}
