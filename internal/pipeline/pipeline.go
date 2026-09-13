// Package pipeline orchestrates the stages: register videos -> analyze ->
// segment -> extract frames -> describe -> score. Every stage is resumable:
// progress lives in SQLite as per-video flags and per-segment statuses, and
// every LLM answer is cached by content hash so reruns never pay twice.
package pipeline

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"longcut/internal/appdirs"
	"longcut/internal/export"
	"longcut/internal/ffmpeg"
	"longcut/internal/frames"
	"longcut/internal/llm"
	"longcut/internal/recdate"
	"longcut/internal/scenes"
	"longcut/internal/store"
)

// Options tunes every stage.
type Options struct {
	Analyze     scenes.AnalyzeOptions
	Seg         scenes.Params
	Frames      frames.Options
	Concurrency int    // parallel frame extractions / description calls
	Limit       int    // max segments to process in this run (0 = all)
	Lang        string // description language
	ContextN    int    // previous segments shown to the scorer
	Force       bool   // re-segment even when parameters did not change
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{
		Analyze:     scenes.AnalyzeOptions{Mode: scenes.ModeKeyframes, FPS: 2, HWAccel: true, MaxKeyframeSpacing: 2.5},
		Seg:         scenes.DefaultParams(),
		Frames:      frames.Options{MaxWidth: 1280, MaxHeight: 720, Quality: 70},
		Concurrency: 4,
		Lang:        "ru",
		ContextN:    8,
	}
}

// Event is a progress notification.
type Event struct {
	Stage   string  `json:"stage"` // probe|analyze|segment|frames|describe|score
	Video   string  `json:"video"` // base name of the current file, if any
	Done    int     `json:"done"`
	Total   int     `json:"total"`
	Message string  `json:"message,omitempty"`
	CostUSD float64 `json:"cost_usd"` // cumulative spend this run (estimated in mock mode)
	Hits    int     `json:"hits"`     // cache hits this run
	// Seg carries the per-segment result that just landed, so a UI can patch
	// its state in place instead of reloading the whole project.
	Seg *SegmentDelta `json:"seg,omitempty"`
}

// SegmentDelta is the freshly persisted change of one segment.
type SegmentDelta struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	Score       *int   `json:"score,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Model       string `json:"model,omitempty"`
}

// Runner executes stages for one project.
type Runner struct {
	Store   *store.Store
	Bins    ffmpeg.Bins
	Dirs    appdirs.Dirs
	LLM     llm.Client
	Project *store.Project
	Opts    Options
	OnEvent func(Event)
	Log     *slog.Logger
	// Mock marks the LLM as offline: results are stored on segments (tagged
	// with model "mock") but never written to the API cache or usage ledger.
	Mock bool

	mu     sync.Mutex
	cost   float64
	hits   int
	errors int
}

func (r *Runner) emit(e Event) {
	r.mu.Lock()
	e.CostUSD, e.Hits = r.cost, r.hits
	r.mu.Unlock()
	if r.OnEvent != nil {
		r.OnEvent(e)
	}
}

func (r *Runner) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

var videoExts = map[string]bool{".mp4": true, ".mkv": true, ".mov": true, ".avi": true, ".webm": true, ".ts": true, ".m2ts": true, ".flv": true}

// ExpandPaths turns files and directories into a sorted list of video files.
func ExpandPaths(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
				continue
			}
			out = append(out, filepath.Join(p, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// AddPaths probes and registers videos in the project.
func (r *Runner) AddPaths(ctx context.Context, paths []string) ([]store.Video, error) {
	files, err := ExpandPaths(paths)
	if err != nil {
		return nil, err
	}
	var added []store.Video
	for i, f := range files {
		abs, _ := filepath.Abs(f)
		r.emit(Event{Stage: "probe", Video: filepath.Base(f), Done: i, Total: len(files)})
		info, err := r.Bins.Probe(ctx, abs)
		if err != nil {
			r.log().Warn("skip file", "file", f, "err", err)
			continue
		}
		key, err := quickHash(abs)
		if err != nil {
			return nil, err
		}
		var mtime time.Time
		if st, err := os.Stat(abs); err == nil {
			mtime = st.ModTime()
		}
		rec, _ := recdate.Guess(abs, info.CreationTime, mtime, time.Duration(info.Duration*float64(time.Second)))
		v := store.Video{ProjectID: r.Project.ID, Path: abs, FileKey: key, Duration: info.Duration, Width: info.Width, Height: info.Height, FPS: info.FPS, Size: info.Size,
			SortOrder: 1<<30 + i, RecordedAt: rec.Format(time.RFC3339)}
		isNew, err := r.Store.UpsertVideo(ctx, &v)
		if err != nil {
			return nil, err
		}
		if isNew {
			r.log().Info("added", "file", filepath.Base(f), "duration", llm.Timecode(info.Duration), "res", fmt.Sprintf("%dx%d@%.0f", info.Width, info.Height, info.FPS))
		}
		added = append(added, v)
	}
	// A longplay is chronological: lay the files out by recording start.
	if err := r.Store.SortVideosByDate(ctx, r.Project.ID); err != nil {
		return nil, err
	}
	r.emit(Event{Stage: "probe", Done: len(files), Total: len(files)})
	return added, nil
}

// quickHash fingerprints a file by size plus its first and last 4 MB, which
// is enough to notice re-encodes without reading tens of gigabytes.
func quickHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const chunk = 4 << 20
	h := sha256.New()
	fmt.Fprintf(h, "%d|", st.Size())
	if _, err := io.CopyN(h, f, chunk); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if st.Size() > 2*chunk {
		if _, err := f.Seek(-chunk, io.SeekEnd); err == nil {
			if _, err := io.CopyN(h, f, chunk); err != nil && !errors.Is(err, io.EOF) {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:24], nil
}

// Analyze runs scene analysis on every unanalyzed video, then (re)segments
// videos whose segmentation parameters changed.
func (r *Runner) Analyze(ctx context.Context) error {
	videos, err := r.Store.ListVideos(ctx, r.Project.ID)
	if err != nil {
		return err
	}
	for i, v := range videos {
		if v.Analyzed {
			continue
		}
		name := filepath.Base(v.Path)
		r.emit(Event{Stage: "analyze", Video: name, Done: i, Total: len(videos)})
		a, err := scenes.Analyze(ctx, r.Bins, v.Path, v.Duration, r.Opts.Analyze, func(done, total float64) {
			r.emit(Event{Stage: "analyze", Video: name, Done: int(done), Total: int(total)})
		})
		if err != nil {
			return fmt.Errorf("analyze %s: %w", name, err)
		}
		blob, err := gzipJSON(a)
		if err != nil {
			return err
		}
		if err := r.Store.SaveAnalysis(ctx, v.ID, blob); err != nil {
			return err
		}
		r.log().Info("analyzed", "file", name, "samples", len(a.Frames), "spacing", fmt.Sprintf("%.2fs", a.MeanSpacing()), "mode", a.Mode)
	}
	return r.Segment(ctx)
}

// Segment builds segments for analyzed videos whose stored parameters differ
// from the current ones (or all, when Force is set).
func (r *Runner) Segment(ctx context.Context) error {
	videos, err := r.Store.ListVideos(ctx, r.Project.ID)
	if err != nil {
		return err
	}
	params := store.JSON(r.Opts.Seg)
	for i, v := range videos {
		if !v.Analyzed || (v.SegParams == params && !r.Opts.Force) {
			continue
		}
		name := filepath.Base(v.Path)
		r.emit(Event{Stage: "segment", Video: name, Done: i, Total: len(videos)})
		blob, err := r.Store.LoadAnalysis(ctx, v.ID)
		if err != nil {
			return err
		}
		var a scenes.Analysis
		if err := gunzipJSON(blob, &a); err != nil {
			return fmt.Errorf("decode analysis of %s: %w", name, err)
		}
		segs := scenes.Segmentize(&a, r.Opts.Seg)
		rows := make([]store.Segment, len(segs))
		for j, s := range segs {
			rows[j] = store.Segment{Start: s.Start, End: s.End, MotionAvg: s.MotionAvg, MotionMax: s.MotionMax, LumaAvg: s.LumaAvg, CutScore: s.CutScore}
		}
		if err := r.Store.ReplaceSegments(ctx, v.ID, rows, params); err != nil {
			return err
		}
		r.log().Info("segmented", "file", name, "segments", len(rows))
	}
	r.emit(Event{Stage: "segment", Done: len(videos), Total: len(videos)})
	return nil
}

// pending returns project segments below the target status, in timeline order,
// honouring Limit.
func (r *Runner) pending(ctx context.Context, below string) ([]store.Segment, map[int64]store.Video, error) {
	videos, err := r.Store.ListVideos(ctx, r.Project.ID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[int64]store.Video{}
	for _, v := range videos {
		byID[v.ID] = v
	}
	all, err := r.Store.ListProjectSegments(ctx, r.Project.ID)
	if err != nil {
		return nil, nil, err
	}
	var out []store.Segment
	for _, s := range all {
		if store.StatusRank(s.Status) < store.StatusRank(below) {
			out = append(out, s)
		}
	}
	if r.Opts.Limit > 0 && len(out) > r.Opts.Limit {
		out = out[:r.Opts.Limit]
	}
	return out, byID, nil
}

// Frames extracts keyframes for segments that do not have them yet.
func (r *Runner) Frames(ctx context.Context) error {
	segs, videos, err := r.pending(ctx, store.StatusFramed)
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		return nil
	}
	r.emit(Event{Stage: "frames", Done: 0, Total: len(segs)})
	var done int
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, r.Opts.Concurrency))
	for _, s := range segs {
		s := s
		g.Go(func() error {
			v := videos[s.VideoID]
			if _, err := r.ensureFrames(gctx, &s, v); err != nil {
				return fmt.Errorf("frames for %s #%d: %w", filepath.Base(v.Path), s.Idx, err)
			}
			r.mu.Lock()
			done++
			d := done
			r.mu.Unlock()
			r.emit(Event{Stage: "frames", Video: filepath.Base(v.Path), Done: d, Total: len(segs)})
			return nil
		})
	}
	return g.Wait()
}

// ensureFrames returns the segment's frames, extracting them if missing.
func (r *Runner) ensureFrames(ctx context.Context, s *store.Segment, v store.Video) ([]frames.Frame, error) {
	if s.FramesJSON != "" {
		var fr []frames.Frame
		if err := json.Unmarshal([]byte(s.FramesJSON), &fr); err == nil && len(fr) > 0 && allExist(fr) {
			return fr, nil
		}
	}
	n := frames.CountFor(s.End - s.Start)
	times := frames.Times(s.Start, s.End, n)
	dir := filepath.Join(r.Dirs.Frames, v.FileKey)
	prefix := fmt.Sprintf("%09d-%09d", int64(s.Start*1000), int64(s.End*1000))
	fr, err := frames.Extract(ctx, r.Bins, v.Path, times, dir, prefix, r.Opts.Frames)
	if err != nil {
		return nil, err
	}
	js := store.JSON(fr)
	if err := r.Store.SetFrames(ctx, s.ID, js); err != nil {
		return nil, err
	}
	s.FramesJSON = js
	if s.Status == store.StatusDetected {
		s.Status = store.StatusFramed
	}
	return fr, nil
}

func allExist(fr []frames.Frame) bool {
	for _, f := range fr {
		if _, err := os.Stat(f.Path); err != nil {
			return false
		}
	}
	return true
}

// Describe sends frames of undescribed segments to the model.
func (r *Runner) Describe(ctx context.Context) error {
	if !r.Mock {
		if err := r.Store.ResetMock(ctx, r.Project.ID); err != nil {
			return err
		}
	}
	segs, videos, err := r.pending(ctx, store.StatusDescribed)
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		return nil
	}
	r.emit(Event{Stage: "describe", Done: 0, Total: len(segs)})
	var done int
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, r.Opts.Concurrency))
	for _, s := range segs {
		s := s
		g.Go(func() error {
			v := videos[s.VideoID]
			name := filepath.Base(v.Path)
			text, err := r.describeOne(gctx, &s, v)
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				r.log().Error("describe failed", "file", name, "seg", s.Idx, "err", err)
				_ = r.Store.SetError(gctx, s.ID, err.Error())
				r.mu.Lock()
				r.errors++
				r.mu.Unlock()
				if isFatal(err) {
					return err
				}
			}
			r.mu.Lock()
			done++
			d := done
			r.mu.Unlock()
			ev := Event{Stage: "describe", Video: name, Done: d, Total: len(segs), Message: fmt.Sprintf("#%d %s", s.Idx, firstLine(text))}
			if err == nil && text != "" {
				ev.Seg = &SegmentDelta{ID: s.ID, Status: store.StatusDescribed, Description: text}
			}
			r.emit(ev)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return r.errSummary()
}

func (r *Runner) describeOne(ctx context.Context, s *store.Segment, v store.Video) (string, error) {
	fr, err := r.ensureFrames(ctx, s, v)
	if err != nil {
		return "", err
	}
	req := llm.DescribeRequest{Game: r.Project.Game, Lang: r.Opts.Lang, Start: s.Start, End: s.End}
	hashes := make([]string, len(fr))
	for i, f := range fr {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return "", err
		}
		req.Frames = append(req.Frames, data)
		req.Times = append(req.Times, f.T)
		hashes[i] = f.SHA256
	}
	model := r.LLM.Model()
	key := llm.DescribeCacheKey(model, req, hashes)
	if !r.Mock {
		if e, err := r.Store.CacheGet(ctx, key); err != nil {
			return "", err
		} else if e != nil {
			var resp struct{ Text string }
			if json.Unmarshal([]byte(e.Response), &resp) == nil && resp.Text != "" {
				r.mu.Lock()
				r.hits++
				r.mu.Unlock()
				return resp.Text, r.Store.SetDescription(ctx, s.ID, resp.Text, e.Model)
			}
		}
	}
	res, err := r.LLM.Describe(ctx, req)
	if err != nil {
		return "", err
	}
	cost := llm.Cost(model, res.Usage)
	r.mu.Lock()
	r.cost += cost
	r.mu.Unlock()
	storedModel := model
	if r.Mock {
		storedModel = "mock"
	} else {
		if err := r.Store.CachePut(ctx, r.Project.ID, store.CacheEntry{
			Key: key, Kind: "describe", Model: model, Response: store.JSON(map[string]string{"text": res.Text}),
			InputTokens: res.Usage.Input, OutputTokens: res.Usage.Output, CacheRead: res.Usage.CacheRead, CacheWrite: res.Usage.CacheWrite, CostUSD: cost,
		}); err != nil {
			return "", err
		}
	}
	return res.Text, r.Store.SetDescription(ctx, s.ID, res.Text, storedModel)
}

// Score rates described segments in timeline order, feeding each call the
// preceding ContextN descriptions (and their scores when available).
func (r *Runner) Score(ctx context.Context) error {
	if !r.Mock {
		if err := r.Store.ResetMock(ctx, r.Project.ID); err != nil {
			return err
		}
	}
	all, err := r.Store.ListProjectSegments(ctx, r.Project.ID)
	if err != nil {
		return err
	}
	var todo []int
	for i, s := range all {
		if s.Status == store.StatusDescribed && s.Description != "" {
			todo = append(todo, i)
		}
	}
	if r.Opts.Limit > 0 && len(todo) > r.Opts.Limit {
		todo = todo[:r.Opts.Limit]
	}
	if len(todo) == 0 {
		return nil
	}
	r.emit(Event{Stage: "score", Done: 0, Total: len(todo)})
	model := r.LLM.Model()
	for n, i := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s := &all[i]
		var ctxItems []llm.ContextItem
		for j := i - 1; j >= 0 && len(ctxItems) < r.Opts.ContextN; j-- {
			p := all[j]
			if p.Description == "" {
				continue
			}
			ctxItems = append(ctxItems, llm.ContextItem{Index: j, Start: p.Start, Description: p.Description, Score: p.Score})
		}
		for a, b := 0, len(ctxItems)-1; a < b; a, b = a+1, b-1 {
			ctxItems[a], ctxItems[b] = ctxItems[b], ctxItems[a]
		}
		req := llm.ScoreRequest{Game: r.Project.Game, Lang: r.Opts.Lang, Current: llm.ContextItem{Index: i, Start: s.Start, Description: s.Description}, Duration: s.End - s.Start, Context: ctxItems}
		key := llm.ScoreCacheKey(model, req)

		var score int
		var reason, storedModel string
		hit := false
		if !r.Mock {
			if e, err := r.Store.CacheGet(ctx, key); err != nil {
				return err
			} else if e != nil {
				var resp struct {
					Score  int
					Reason string
				}
				if json.Unmarshal([]byte(e.Response), &resp) == nil && resp.Score >= 1 {
					score, reason, storedModel, hit = resp.Score, resp.Reason, e.Model, true
					r.mu.Lock()
					r.hits++
					r.mu.Unlock()
				}
			}
		}
		if !hit {
			res, err := r.LLM.Score(ctx, req)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.log().Error("score failed", "seg", i, "err", err)
				_ = r.Store.SetError(ctx, s.ID, err.Error())
				r.mu.Lock()
				r.errors++
				r.mu.Unlock()
				if isFatal(err) {
					return err
				}
				continue
			}
			cost := llm.Cost(model, res.Usage)
			r.mu.Lock()
			r.cost += cost
			r.mu.Unlock()
			score, reason, storedModel = res.Score, res.Reason, model
			if r.Mock {
				storedModel = "mock"
			} else if err := r.Store.CachePut(ctx, r.Project.ID, store.CacheEntry{
				Key: key, Kind: "score", Model: model, Response: store.JSON(map[string]any{"score": res.Score, "reason": res.Reason, "raw": res.Raw}),
				InputTokens: res.Usage.Input, OutputTokens: res.Usage.Output, CacheRead: res.Usage.CacheRead, CacheWrite: res.Usage.CacheWrite, CostUSD: cost,
			}); err != nil {
				return err
			}
		}
		if err := r.Store.SetScore(ctx, s.ID, score, reason, storedModel); err != nil {
			return err
		}
		s.Score = &score
		s.Status = store.StatusScored
		sc := score
		r.emit(Event{Stage: "score", Done: n + 1, Total: len(todo), Message: fmt.Sprintf("#%d -> %d (%s)", i, score, reason),
			Seg: &SegmentDelta{ID: s.ID, Status: store.StatusScored, Score: &sc, Reason: reason, Model: storedModel}})
	}
	return r.errSummary()
}

// BuildPlan selects segments for export: explicit Selected overrides win,
// otherwise the effective score (manual override, else model score) must be
// >= threshold. Segments without any score are excluded unless selected.
func (r *Runner) BuildPlan(ctx context.Context, threshold int) (export.Plan, error) {
	videos, err := r.Store.ListVideos(ctx, r.Project.ID)
	if err != nil {
		return export.Plan{}, err
	}
	byID := map[int64]store.Video{}
	for _, v := range videos {
		byID[v.ID] = v
	}
	segs, err := r.Store.ListProjectSegments(ctx, r.Project.ID)
	if err != nil {
		return export.Plan{}, err
	}
	plan := export.Plan{Title: r.Project.Name}
	for _, s := range segs {
		keep := false
		switch {
		case s.Selected != nil:
			keep = *s.Selected
		case s.Score != nil || s.ScoreManual != nil:
			keep = s.EffectiveScore() >= threshold
		}
		if !keep {
			continue
		}
		v := byID[s.VideoID]
		plan.Clips = append(plan.Clips, export.Clip{
			Path: v.Path, Start: s.Start, End: s.End, FPS: v.FPS, Width: v.Width, Height: v.Height, Duration: v.Duration,
			Label: fmt.Sprintf("#%d score %d: %s", s.Idx, s.EffectiveScore(), firstLine(s.Description)),
			Notes: []string{fmt.Sprintf("#%d [%s-%s] score %d: %s", s.Idx, llm.Timecode(s.Start), llm.Timecode(s.End), s.EffectiveScore(), strings.TrimSpace(s.Description))},
		})
	}
	// Merge adjacent clips of the same file so copy-mode output has fewer
	// splice points.
	var merged []export.Clip
	for _, c := range plan.Clips {
		if n := len(merged); n > 0 && merged[n-1].Path == c.Path && math.Abs(merged[n-1].End-c.Start) < 0.002 {
			merged[n-1].End = c.End
			merged[n-1].Notes = append(merged[n-1].Notes, c.Notes...)
			continue
		}
		merged = append(merged, c)
	}
	plan.Clips = merged
	return plan, nil
}

// Estimate summarises remaining work and expected spend.
type Estimate struct {
	Model            string  `json:"model"`
	Segments         int     `json:"segments"`
	ToDescribe       int     `json:"to_describe"`
	ToScore          int     `json:"to_score"`
	DescribeUSD      float64 `json:"describe_usd"`
	ScoreUSD         float64 `json:"score_usd"`
	RemainingUSD     float64 `json:"remaining_usd"`
	SpentUSD         float64 `json:"spent_usd"`
	SpentCalls       int64   `json:"spent_calls"`
	SpentInputTokens int64   `json:"spent_input_tokens"`
	UnknownPricing   bool    `json:"unknown_pricing"`
}

// Estimate computes the expected cost of finishing the project.
func (r *Runner) Estimate(ctx context.Context) (Estimate, error) {
	all, err := r.Store.ListProjectSegments(ctx, r.Project.ID)
	if err != nil {
		return Estimate{}, err
	}
	model := r.LLM.Model()
	_, known := llm.PriceFor(model)
	est := Estimate{Model: model, Segments: len(all), UnknownPricing: !known}
	w, h := r.Opts.Frames.MaxWidth, r.Opts.Frames.MaxHeight
	for _, s := range all {
		if store.StatusRank(s.Status) < store.StatusRank(store.StatusDescribed) || s.DescModel == "mock" {
			est.ToDescribe++
			est.DescribeUSD += llm.Cost(model, llm.EstimateDescribe(frames.CountFor(s.End-s.Start), w, h))
		}
		if store.StatusRank(s.Status) < store.StatusRank(store.StatusScored) || s.ScoreModel == "mock" {
			est.ToScore++
			est.ScoreUSD += llm.Cost(model, llm.EstimateScore(r.Opts.ContextN))
		}
	}
	est.RemainingUSD = est.DescribeUSD + est.ScoreUSD
	usage, err := r.Store.ProjectUsage(ctx, r.Project.ID)
	if err != nil {
		return Estimate{}, err
	}
	tot := usage[""]
	est.SpentUSD, est.SpentCalls, est.SpentInputTokens = tot.CostUSD, tot.Calls, tot.InputTokens
	return est, nil
}

// SessionCost returns spend (or estimated spend in mock mode) for this run.
func (r *Runner) SessionCost() (float64, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cost, r.hits
}

func (r *Runner) errSummary() error {
	r.mu.Lock()
	n := r.errors
	r.errors = 0
	r.mu.Unlock()
	if n > 0 {
		return fmt.Errorf("%d segment(s) failed; rerun to retry them", n)
	}
	return nil
}

// isFatal marks errors that will not go away by moving on to the next
// segment (bad API key, missing ffmpeg, out of credit).
func isFatal(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"401", "403", "authentication", "invalid x-api-key", "credit balance", "ffmpeg not found", "giving up after"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > 90 {
		s = string([]rune(s)[:90]) + "…"
	}
	return s
}

func gzipJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipJSON(blob []byte, v any) error {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return err
	}
	defer zr.Close()
	return json.NewDecoder(zr).Decode(v)
}
