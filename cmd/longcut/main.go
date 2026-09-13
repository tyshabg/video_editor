// Command longcut is the CLI front to the Longcut pipeline: register videos,
// detect scenes, describe and score segments with Claude, and inspect results.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"

	"longcut/internal/appdirs"
	"longcut/internal/export"
	"longcut/internal/ffmpeg"
	"longcut/internal/llm"
	"longcut/internal/pipeline"
	"longcut/internal/scenes"
	"longcut/internal/store"
)

const usage = `longcut - longplay segmentation and narrative scoring

Usage: longcut <command> [flags] [args]

Commands:
  add       -p NAME [-game G] PATH...   register video files or folders in a project
  analyze   -p NAME                     scene analysis + segmentation (resumable)
  frames    -p NAME                     extract keyframes for all segments
  describe  -p NAME [-limit N] [-mock]  describe segments with Claude
  score     -p NAME [-limit N] [-mock]  rate narrative importance with context
  run       -p NAME [-game G] PATH...   add + analyze + frames + describe + score
  status    -p NAME                     progress, spend and remaining cost estimate
  segments  -p NAME [-min-score N] [-json]  list segments with descriptions and scores
  export    -p NAME -out FILE.mp4 [-threshold N] [-encode] [-edl] [-xml] [-plan]
                                        render selected segments (+ EDL / FCP7 XML for Premiere & Resolve)
  export    -p NAME -out FILE.edl -no-video [-xml]   write only the edit list, no render
  projects                              list projects
  datadir   [PATH]                      show the data folder, or set it (saved in config; LONGCUT_DATA_DIR still wins)
  doctor                                check ffmpeg, data dir and API key

Common flags (accepted by every command):
  -p NAME          project name
  -game NAME       game title used in prompts (stored on the project)
  -data DIR        data directory (default: LONGCUT_DATA_DIR or the OS app dir)
  -model ID        Claude model (default ` + llm.DefaultModel + `)
  -lang CODE       description language: ru, en (default ru)
  -concurrency N   parallel ffmpeg/API workers (default 4)
  -min/-max SEC    segment length bounds (default 5/60)
  -cut F           hard-cut threshold 0..1 (default 0.30)
  -sample MODE     keyframes | fps (default keyframes)
  -fps F           sample rate for -sample fps (default 2)
  -no-hwaccel      disable GPU decoding
  -force           re-segment even if parameters are unchanged
  -mock            offline mode: no API calls, estimated costs only
  -limit N         process at most N segments this run
  -v               verbose logs
`

type common struct {
	project, game, data, model, lang, sample string
	concurrency, limit                       int
	minLen, maxLen, cut, fps                 float64
	noHW, force, mock, verbose               bool
}

func addCommon(fs *flag.FlagSet, c *common) {
	fs.StringVar(&c.project, "p", "", "project name")
	fs.StringVar(&c.game, "game", "", "game title")
	fs.StringVar(&c.data, "data", "", "data directory")
	fs.StringVar(&c.model, "model", llm.DefaultModel, "model id")
	fs.StringVar(&c.lang, "lang", "ru", "description language")
	fs.StringVar(&c.sample, "sample", "keyframes", "keyframes|fps")
	fs.IntVar(&c.concurrency, "concurrency", 4, "workers")
	fs.IntVar(&c.limit, "limit", 0, "max segments this run")
	fs.Float64Var(&c.minLen, "min", 5, "min segment seconds")
	fs.Float64Var(&c.maxLen, "max", 60, "max segment seconds")
	fs.Float64Var(&c.cut, "cut", 0.30, "hard cut threshold")
	fs.Float64Var(&c.fps, "fps", 2, "sample fps")
	fs.BoolVar(&c.noHW, "no-hwaccel", false, "disable hwaccel")
	fs.BoolVar(&c.force, "force", false, "force re-segmentation")
	fs.BoolVar(&c.mock, "mock", false, "offline mock LLM")
	fs.BoolVar(&c.verbose, "v", false, "verbose")
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var c common
	addCommon(fs, &c)
	var minScore int
	var asJSON, reset bool
	var ex exportFlags
	switch cmd {
	case "segments":
		fs.IntVar(&minScore, "min-score", 0, "only segments with effective score >= N")
		fs.BoolVar(&asJSON, "json", false, "JSON output")
	case "score":
		fs.BoolVar(&reset, "reset", false, "discard model scores and re-score everything")
	case "export":
		fs.StringVar(&ex.out, "out", "", "output video path (.mp4)")
		fs.IntVar(&ex.threshold, "threshold", 5, "keep segments with effective score >= N")
		fs.BoolVar(&ex.encode, "encode", false, "re-encode (frame-accurate) instead of stream copy")
		fs.IntVar(&ex.height, "height", 0, "downscale to this height when re-encoding (e.g. 1080)")
		fs.BoolVar(&ex.edl, "edl", false, "also write CMX 3600 EDL next to the output")
		fs.BoolVar(&ex.xml, "xml", false, "also write FCP7 XML (Premiere/Resolve) next to the output")
		fs.BoolVar(&ex.planOnly, "plan", false, "print the clip list and exit without rendering")
		fs.BoolVar(&ex.noVideo, "no-video", false, "write only EDL/XML (implies -edl unless -xml given); -out may be a .edl/.xml path")
	}
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, cmd, c, fs.Args(), minScore, asJSON, reset, ex); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\ninterrupted; progress is saved, rerun to continue")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type exportFlags struct {
	out               string
	threshold, height int
	encode, edl, xml  bool
	planOnly, noVideo bool
}

func run(ctx context.Context, cmd string, c common, args []string, minScore int, asJSON, reset bool, ex exportFlags) error {
	if c.data != "" {
		os.Setenv("LONGCUT_DATA_DIR", c.data)
	}
	dirs, err := appdirs.Resolve()
	if err != nil {
		return err
	}
	ui := newUI(c.verbose)
	defer ui.close()
	logger := ui.logger()

	if cmd == "doctor" {
		return doctor(ctx, dirs, ui)
	}
	if cmd == "datadir" {
		if len(args) == 0 {
			fmt.Printf("%s (%s)\n", dirs.Root, dirs.Source)
			return nil
		}
		target, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		if _, err := appdirs.ResolveAt(target); err != nil {
			return err
		}
		if err := appdirs.SaveConfig(appdirs.Config{DataDir: target}); err != nil {
			return err
		}
		fmt.Printf("data dir set to %s (existing data was not moved; use the app's Settings to move it)\n", target)
		if appdirs.EnvOverride() {
			fmt.Println("note: LONGCUT_DATA_DIR is set in this shell and takes precedence")
		}
		return nil
	}
	if cmd == "projects" {
		st, err := store.Open(dirs.DBPath)
		if err != nil {
			return err
		}
		defer st.Close()
		ps, err := st.ListProjects(ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			counts, _ := st.Counts(ctx, p.ID)
			total := 0
			for _, n := range counts {
				total += n
			}
			fmt.Printf("%-24s game=%-16s segments=%d scored=%d updated=%s\n", p.Name, p.Game, total, counts[store.StatusScored], p.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
		return nil
	}
	if c.project == "" {
		return fmt.Errorf("-p PROJECT is required")
	}
	bins, err := ffmpeg.Find(dirs.Bin)
	if err != nil {
		return err
	}
	st, err := store.Open(dirs.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	project, err := st.GetOrCreateProject(ctx, c.project, c.game)
	if err != nil {
		return err
	}

	opts := pipeline.DefaultOptions()
	opts.Seg.MinLen, opts.Seg.MaxLen, opts.Seg.CutThreshold = c.minLen, c.maxLen, c.cut
	opts.Analyze.Mode = scenes.SampleMode(c.sample)
	opts.Analyze.FPS = c.fps
	opts.Analyze.HWAccel = !c.noHW
	opts.Concurrency, opts.Limit, opts.Lang, opts.Force = c.concurrency, c.limit, c.lang, c.force

	var client llm.Client
	if c.mock {
		client = llm.NewMock(c.model)
	} else {
		client = llm.NewAnthropic(c.model)
	}
	r := &pipeline.Runner{Store: st, Bins: bins, Dirs: dirs, LLM: client, Project: project, Opts: opts, OnEvent: ui.onEvent, Log: logger, Mock: c.mock}
	needsKey := func() error {
		if !c.mock && os.Getenv("ANTHROPIC_API_KEY") == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY is not set (use -mock for an offline dry run)")
		}
		return nil
	}

	start := time.Now()
	switch cmd {
	case "add":
		if len(args) == 0 {
			return fmt.Errorf("add: give at least one file or folder")
		}
		_, err = r.AddPaths(ctx, args)
	case "analyze":
		err = r.Analyze(ctx)
	case "frames":
		err = r.Frames(ctx)
	case "describe":
		if err = needsKey(); err == nil {
			err = r.Describe(ctx)
		}
	case "score":
		if err = needsKey(); err == nil {
			if reset {
				if err = st.ResetScores(ctx, project.ID); err != nil {
					return err
				}
			}
			err = r.Score(ctx)
		}
	case "run":
		if err = needsKey(); err != nil {
			return err
		}
		if len(args) > 0 {
			if _, err = r.AddPaths(ctx, args); err != nil {
				return err
			}
		}
		for _, step := range []func(context.Context) error{r.Analyze, r.Frames, r.Describe, r.Score} {
			if err = step(ctx); err != nil {
				break
			}
		}
	case "status":
		return status(ctx, st, r, project, ui)
	case "segments":
		return listSegments(ctx, st, project, minScore, asJSON)
	case "export":
		return doExport(ctx, r, ex, ui)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	ui.finish()
	cost, hits := r.SessionCost()
	label := "spent"
	if c.mock {
		label = "estimated"
	}
	if cost > 0 || hits > 0 {
		fmt.Fprintf(os.Stderr, "%s this run: $%.4f, cache hits: %d, elapsed %s\n", label, cost, hits, time.Since(start).Round(time.Second))
	}
	if err != nil {
		return err
	}
	if cmd != "add" {
		return status(ctx, st, r, project, ui)
	}
	return nil
}

func status(ctx context.Context, st *store.Store, r *pipeline.Runner, p *store.Project, ui *ui) error {
	ui.finish()
	videos, err := st.ListVideos(ctx, p.ID)
	if err != nil {
		return err
	}
	counts, err := st.Counts(ctx, p.ID)
	if err != nil {
		return err
	}
	total := 0
	var dur float64
	for _, n := range counts {
		total += n
	}
	analyzed := 0
	for _, v := range videos {
		dur += v.Duration
		if v.Analyzed {
			analyzed++
		}
	}
	est, err := r.Estimate(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("project %q (game: %s)\n", p.Name, p.Game)
	fmt.Printf("  videos:    %d (%d analyzed), total %s\n", len(videos), analyzed, llm.Timecode(dur))
	fmt.Printf("  segments:  %d  detected=%d framed=%d described=%d scored=%d\n", total,
		counts[store.StatusDetected], counts[store.StatusFramed], counts[store.StatusDescribed], counts[store.StatusScored])
	fmt.Printf("  spent:     $%.4f over %d API calls (%d input tokens)\n", est.SpentUSD, est.SpentCalls, est.SpentInputTokens)
	fmt.Printf("  remaining: ~$%.2f with %s  (describe %d seg ≈ $%.2f, score %d seg ≈ $%.2f)\n",
		est.RemainingUSD, est.Model, est.ToDescribe, est.DescribeUSD, est.ToScore, est.ScoreUSD)
	if est.UnknownPricing {
		fmt.Printf("  note: no price table for %s, Sonnet 4.6 rates assumed\n", est.Model)
	}
	return nil
}

func listSegments(ctx context.Context, st *store.Store, p *store.Project, minScore int, asJSON bool) error {
	videos, err := st.ListVideos(ctx, p.ID)
	if err != nil {
		return err
	}
	names := map[int64]string{}
	for _, v := range videos {
		names[v.ID] = filepath.Base(v.Path)
	}
	segs, err := st.ListProjectSegments(ctx, p.ID)
	if err != nil {
		return err
	}
	if asJSON {
		type row struct {
			store.Segment
			Video string `json:"video"`
		}
		var rows []row
		for _, s := range segs {
			if s.EffectiveScore() < minScore {
				continue
			}
			s.FramesJSON = ""
			rows = append(rows, row{s, names[s.VideoID]})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	lastVideo := int64(-1)
	for _, s := range segs {
		if s.EffectiveScore() < minScore {
			continue
		}
		if s.VideoID != lastVideo {
			fmt.Printf("\n== %s ==\n", names[s.VideoID])
			lastVideo = s.VideoID
		}
		score := "  -"
		if s.Score != nil || s.ScoreManual != nil {
			score = fmt.Sprintf("%3d", s.EffectiveScore())
			if s.ScoreManual != nil {
				score += "*"
			}
		}
		fmt.Printf("#%-4d %s-%s %4.0fs mot=%.3f [%s] %s\n", s.Idx, llm.Timecode(s.Start), llm.Timecode(s.End), s.End-s.Start, s.MotionAvg, score, s.Status)
		if s.Description != "" {
			fmt.Printf("      %s\n", s.Description)
		}
		if s.ScoreReason != "" {
			fmt.Printf("      -> %s\n", s.ScoreReason)
		}
		if s.Error != "" {
			fmt.Printf("      !! %s\n", s.Error)
		}
	}
	return nil
}

func doExport(ctx context.Context, r *pipeline.Runner, ex exportFlags, ui *ui) error {
	plan, err := r.BuildPlan(ctx, ex.threshold)
	if err != nil {
		return err
	}
	if len(plan.Clips) == 0 {
		return fmt.Errorf("no segments with score >= %d (or selected); lower -threshold or score the project first", ex.threshold)
	}
	fmt.Printf("export plan: %d clips, %s total, threshold %d\n", len(plan.Clips), llm.Timecode(plan.TotalDuration()), ex.threshold)
	for i, cl := range plan.Clips {
		fmt.Printf("  %3d  %-34s %s-%s %5.0fs  %s\n", i+1, trunc(filepath.Base(cl.Path), 34), llm.Timecode(cl.Start), llm.Timecode(cl.End), cl.Len(), trunc(cl.Label, 70))
	}
	if ex.planOnly {
		return nil
	}
	if ex.out == "" {
		return fmt.Errorf("-out FILE is required (or use -plan)")
	}
	if ex.noVideo {
		// The output extension picks a format too, on top of explicit flags.
		switch strings.ToLower(filepath.Ext(ex.out)) {
		case ".edl":
			ex.edl = true
		case ".xml":
			ex.xml = true
		}
		if !ex.edl && !ex.xml {
			ex.edl = true
		}
	}
	if err := os.MkdirAll(filepath.Dir(ex.out), 0o755); err != nil {
		return err
	}
	base := strings.TrimSuffix(ex.out, filepath.Ext(ex.out))
	if ex.edl {
		p := base + ".edl"
		if err := os.WriteFile(p, []byte(export.EDL(plan)), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", p)
	}
	if ex.xml {
		p := base + ".xml"
		if err := os.WriteFile(p, []byte(export.XMEML(plan)), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", p)
	}
	if ex.noVideo {
		return nil
	}
	mode := export.ModeCopy
	if ex.encode {
		mode = export.ModeEncode
	}
	start := time.Now()
	err = export.Render(ctx, r.Bins, plan, ex.out, export.RenderOptions{Mode: mode, Height: ex.height}, func(done, total float64) {
		ui.onEvent(pipeline.Event{Stage: "render " + string(mode), Done: int(done), Total: int(total)})
	})
	ui.finish()
	if err != nil {
		if mode == export.ModeCopy {
			return fmt.Errorf("%w\nstream copy failed; retry with -encode", err)
		}
		return err
	}
	st, _ := os.Stat(ex.out)
	var mb int64
	if st != nil {
		mb = st.Size() >> 20
	}
	fmt.Printf("wrote %s (%d MB) in %s\n", ex.out, mb, time.Since(start).Round(time.Second))
	return nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func doctor(ctx context.Context, dirs appdirs.Dirs, ui *ui) error {
	fmt.Printf("data dir:  %s\n", dirs.Root)
	bins, err := ffmpeg.Find(dirs.Bin)
	if err != nil {
		fmt.Printf("ffmpeg:    MISSING (%v)\n", err)
	} else {
		ver, _ := bins.Version(ctx)
		fmt.Printf("ffmpeg:    %s\n           %s\n", bins.FFmpeg, ver)
	}
	if k := os.Getenv("ANTHROPIC_API_KEY"); k == "" {
		fmt.Println("api key:   ANTHROPIC_API_KEY not set")
	} else {
		fmt.Printf("api key:   set (%s…%s)\n", k[:min(10, len(k))], k[max(0, len(k)-4):])
	}
	st, err := store.Open(dirs.DBPath)
	if err != nil {
		fmt.Printf("database:  ERROR %v\n", err)
		return nil
	}
	defer st.Close()
	ps, _ := st.ListProjects(ctx)
	fmt.Printf("database:  %s (%d projects)\n", dirs.DBPath, len(ps))
	return nil
}

// ---- terminal UI: one progress bar plus log lines that do not garble it ----

type ui struct {
	mu      sync.Mutex
	bar     *progressbar.ProgressBar
	stage   string
	verbose bool
	out     io.Writer
}

func newUI(verbose bool) *ui { return &ui{verbose: verbose, out: os.Stderr} }

func (u *ui) logger() *slog.Logger {
	lvl := slog.LevelInfo
	if u.verbose {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(uiWriter{u}, &slog.HandlerOptions{Level: lvl, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
}

type uiWriter struct{ u *ui }

func (w uiWriter) Write(p []byte) (int, error) {
	w.u.mu.Lock()
	defer w.u.mu.Unlock()
	if w.u.bar != nil {
		_ = w.u.bar.Clear()
	}
	n, err := w.u.out.Write(p)
	if w.u.bar != nil {
		_ = w.u.bar.RenderBlank()
	}
	return n, err
}

func (u *ui) onEvent(e pipeline.Event) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if e.Stage != u.stage || u.bar == nil {
		if u.bar != nil {
			_ = u.bar.Clear()
		}
		u.stage = e.Stage
		u.bar = progressbar.NewOptions(max(e.Total, 1),
			progressbar.OptionSetWriter(u.out),
			progressbar.OptionSetDescription(stageLabel(e)),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWidth(28),
			progressbar.OptionThrottle(100*time.Millisecond),
			progressbar.OptionSetPredictTime(true),
			progressbar.OptionClearOnFinish(),
		)
	}
	if e.Total > 0 {
		u.bar.ChangeMax(e.Total)
	}
	u.bar.Describe(stageLabel(e))
	_ = u.bar.Set(e.Done)
	if e.Message != "" && (u.verbose || e.Stage == "score" || e.Stage == "describe") {
		_ = u.bar.Clear()
		fmt.Fprintln(u.out, "  "+e.Message)
		_ = u.bar.RenderBlank()
	}
}

func stageLabel(e pipeline.Event) string {
	label := e.Stage
	if e.Video != "" {
		v := e.Video
		if len([]rune(v)) > 28 {
			v = string([]rune(v)[:27]) + "…"
		}
		label += " " + v
	}
	if e.CostUSD > 0 || e.Hits > 0 {
		label += fmt.Sprintf(" $%.3f", e.CostUSD)
		if e.Hits > 0 {
			label += fmt.Sprintf(" (%d cached)", e.Hits)
		}
	}
	return strings.TrimSpace(label)
}

func (u *ui) finish() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.bar != nil {
		_ = u.bar.Clear()
		u.bar = nil
		u.stage = ""
	}
}

func (u *ui) close() { u.finish() }
