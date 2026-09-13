package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"longcut/internal/appdirs"
	"longcut/internal/export"
	"longcut/internal/ffmpeg"
	"longcut/internal/frames"
	"longcut/internal/llm"
	"longcut/internal/pipeline"
	"longcut/internal/proxy"
	"longcut/internal/recdate"
	"longcut/internal/store"
)

// App is the Wails-bound backend.
type App struct {
	ctx   context.Context
	dirs  appdirs.Dirs
	store *store.Store
	bins  ffmpeg.Bins
	binsE error

	mu      sync.Mutex
	project *store.Project
	model   string
	lang    string
	running bool
	cancel  context.CancelFunc
	log     *slog.Logger

	keyFromConfig bool
	keySession    bool
}

func NewApp() *App {
	return &App{model: llm.DefaultModel, lang: "ru", log: slog.Default()}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	dirs, err := appdirs.Resolve()
	if err != nil {
		runtime.MessageDialog(ctx, runtime.MessageDialogOptions{Type: runtime.ErrorDialog, Title: "Longcut", Message: "Cannot create data directory: " + err.Error()})
		return
	}
	a.dirs = dirs
	if f, err := os.OpenFile(filepath.Join(dirs.Logs, "longcut.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		a.log = slog.New(slog.NewTextHandler(f, nil))
	}
	st, err := store.Open(dirs.DBPath)
	if err != nil {
		runtime.MessageDialog(ctx, runtime.MessageDialogOptions{Type: runtime.ErrorDialog, Title: "Longcut", Message: "Cannot open database: " + err.Error()})
		return
	}
	a.store = st
	a.bins, a.binsE = ffmpeg.Find(dirs.Bin)
	// A key remembered in the app config is used when the environment has none.
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		if cfg, err := appdirs.LoadConfig(); err == nil && cfg.APIKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", cfg.APIKey)
			a.keyFromConfig = true
		}
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.Cancel()
	if a.store != nil {
		a.store.Close()
	}
}

// ---- views ----

type ProjectSummary struct {
	Name     string  `json:"name"`
	Game     string  `json:"game"`
	Videos   int     `json:"videos"`
	Segments int     `json:"segments"`
	Scored   int     `json:"scored"`
	Duration float64 `json:"duration"`
	SpentUSD float64 `json:"spent_usd"`
	Updated  string  `json:"updated"`
}

type VideoView struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Duration   float64 `json:"duration"`
	Offset     float64 `json:"offset"` // position on the project timeline
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        float64 `json:"fps"`
	Analyzed   bool    `json:"analyzed"`
	Segments   int     `json:"segments"`
	RecordedAt string  `json:"recorded_at"`
}

type SegmentView struct {
	ID          int64    `json:"id"`
	VideoID     int64    `json:"video_id"`
	Idx         int      `json:"idx"`
	Start       float64  `json:"start"`
	End         float64  `json:"end"`
	Offset      float64  `json:"offset"` // absolute timeline position of Start
	MotionAvg   float64  `json:"motion_avg"`
	CutScore    float64  `json:"cut_score"`
	Status      string   `json:"status"`
	Description string   `json:"description"`
	Score       *int     `json:"score"`
	ScoreReason string   `json:"score_reason"`
	ScoreManual *int     `json:"score_manual"`
	Selected    *bool    `json:"selected"`
	Thumbs      []string `json:"thumbs"`
	Error       string   `json:"error"`
}

type ProjectView struct {
	Name     string                 `json:"name"`
	Game     string                 `json:"game"`
	Videos   []VideoView            `json:"videos"`
	Segments []SegmentView          `json:"segments"`
	Duration float64                `json:"duration"`
	Usage    map[string]store.Usage `json:"usage"`
	Estimate pipeline.Estimate      `json:"estimate"`
}

type SettingsView struct {
	DataDir       string `json:"data_dir"`
	DataDirSource string `json:"data_dir_source"` // env | config | default
	DataDirEnv    bool   `json:"data_dir_env"`    // LONGCUT_DATA_DIR set: UI change disabled
	ConfigPath    string `json:"config_path"`
	FFmpegPath    string `json:"ffmpeg_path"`
	FFmpegVersion string `json:"ffmpeg_version"`
	FFmpegError   string `json:"ffmpeg_error"`
	APIKeySet     bool   `json:"api_key_set"`
	APIKeyHint    string `json:"api_key_hint"`
	APIKeySource  string `json:"api_key_source"` // env | config | session | ""
	Model         string `json:"model"`
	Lang          string `json:"lang"`
	Running       bool   `json:"running"`
	Version       string `json:"version"`
}

// ---- projects ----

func (a *App) ListProjects() ([]ProjectSummary, error) {
	if a.store == nil {
		return nil, errors.New("database not open")
	}
	ps, err := a.store.ListProjects(a.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectSummary, 0, len(ps))
	for _, p := range ps {
		counts, _ := a.store.Counts(a.ctx, p.ID)
		videos, _ := a.store.ListVideos(a.ctx, p.ID)
		usage, _ := a.store.ProjectUsage(a.ctx, p.ID)
		s := ProjectSummary{Name: p.Name, Game: p.Game, Videos: len(videos), Scored: counts[store.StatusScored], Updated: p.UpdatedAt.Local().Format("2006-01-02 15:04")}
		for _, n := range counts {
			s.Segments += n
		}
		for _, v := range videos {
			s.Duration += v.Duration
		}
		s.SpentUSD = usage[""].CostUSD
		out = append(out, s)
	}
	return out, nil
}

func (a *App) CreateProject(name, game string) (*ProjectView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project name is empty")
	}
	if _, err := a.store.GetOrCreateProject(a.ctx, name, strings.TrimSpace(game)); err != nil {
		return nil, err
	}
	return a.OpenProject(name)
}

func (a *App) OpenProject(name string) (*ProjectView, error) {
	p, err := a.store.ProjectByName(a.ctx, name)
	if err != nil {
		return nil, fmt.Errorf("project %q: %w", name, err)
	}
	a.mu.Lock()
	a.project = p
	a.mu.Unlock()
	return a.view()
}

func (a *App) SetGame(game string) (*ProjectView, error) {
	p := a.current()
	if p == nil {
		return nil, errors.New("no project open")
	}
	if _, err := a.store.GetOrCreateProject(a.ctx, p.Name, strings.TrimSpace(game)); err != nil {
		return nil, err
	}
	return a.OpenProject(p.Name)
}

func (a *App) DeleteProject(name string) error {
	p, err := a.store.ProjectByName(a.ctx, name)
	if err != nil {
		return err
	}
	if cur := a.current(); cur != nil && cur.ID == p.ID {
		a.mu.Lock()
		a.project = nil
		a.mu.Unlock()
	}
	return a.store.DeleteProject(a.ctx, p.ID)
}

func (a *App) current() *store.Project {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.project
}

// Refresh returns the current project's state.
func (a *App) Refresh() (*ProjectView, error) { return a.view() }

func (a *App) view() (*ProjectView, error) {
	p := a.current()
	if p == nil {
		return nil, errors.New("no project open")
	}
	videos, err := a.store.ListVideos(a.ctx, p.ID)
	if err != nil {
		return nil, err
	}
	segs, err := a.store.ListProjectSegments(a.ctx, p.ID)
	if err != nil {
		return nil, err
	}
	pv := &ProjectView{Name: p.Name, Game: p.Game, Videos: []VideoView{}, Segments: []SegmentView{}}
	offsets := map[int64]float64{}
	keys := map[int64]string{}
	segCount := map[int64]int{}
	for _, s := range segs {
		segCount[s.VideoID]++
	}
	var off float64
	for i := range videos {
		v := &videos[i]
		// Videos registered before recording dates existed: fill them in from
		// the file name or the file's modification time.
		if v.RecordedAt == "" {
			var mtime time.Time
			if st, err := os.Stat(v.Path); err == nil {
				mtime = st.ModTime()
			}
			if !mtime.IsZero() || func() bool { _, ok := recdate.FromName(v.Path); return ok }() {
				rec, _ := recdate.Guess(v.Path, time.Time{}, mtime, time.Duration(v.Duration*float64(time.Second)))
				v.RecordedAt = rec.Format(time.RFC3339)
				_ = a.store.SetRecordedAt(a.ctx, v.ID, v.RecordedAt)
			}
		}
	}
	for _, v := range videos {
		offsets[v.ID] = off
		keys[v.ID] = v.FileKey
		pv.Videos = append(pv.Videos, VideoView{ID: v.ID, Name: filepath.Base(v.Path), Path: v.Path, Duration: v.Duration, Offset: off, Width: v.Width, Height: v.Height, FPS: v.FPS, Analyzed: v.Analyzed, Segments: segCount[v.ID], RecordedAt: v.RecordedAt})
		off += v.Duration
	}
	pv.Duration = off
	for _, s := range segs {
		sv := SegmentView{ID: s.ID, VideoID: s.VideoID, Idx: s.Idx, Start: s.Start, End: s.End, Offset: offsets[s.VideoID] + s.Start,
			MotionAvg: s.MotionAvg, CutScore: s.CutScore, Status: s.Status, Description: s.Description, Score: s.Score, ScoreReason: s.ScoreReason,
			ScoreManual: s.ScoreManual, Selected: s.Selected, Error: s.Error}
		if s.FramesJSON != "" {
			var fr []frames.Frame
			if json.Unmarshal([]byte(s.FramesJSON), &fr) == nil {
				for _, f := range fr {
					if rel, err := filepath.Rel(a.dirs.Frames, f.Path); err == nil {
						sv.Thumbs = append(sv.Thumbs, "/media/frame/"+filepath.ToSlash(rel))
					}
				}
			}
		}
		pv.Segments = append(pv.Segments, sv)
	}
	pv.Usage, _ = a.store.ProjectUsage(a.ctx, p.ID)
	if r := a.runner(llm.NewMock(a.model), true); r != nil {
		pv.Estimate, _ = r.Estimate(a.ctx)
	}
	return pv, nil
}

// ---- videos ----

func (a *App) PickVideoFiles() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Add recordings",
		Filters: []runtime.FileFilter{{DisplayName: "Video (*.mp4;*.mkv;*.mov;*.avi;*.webm;*.ts)", Pattern: "*.mp4;*.mkv;*.mov;*.avi;*.webm;*.ts;*.m2ts;*.flv"}},
	})
}

func (a *App) PickVideoFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Add a folder of recordings"})
}

func (a *App) AddPaths(paths []string) (*ProjectView, error) {
	if a.binsE != nil {
		return nil, a.binsE
	}
	r := a.runner(llm.NewMock(a.model), true)
	if r == nil {
		return nil, errors.New("no project open")
	}
	if _, err := r.AddPaths(a.ctx, paths); err != nil {
		return nil, err
	}
	return a.view()
}

// SortVideosByDate lays the project's files out chronologically.
func (a *App) SortVideosByDate() (*ProjectView, error) {
	p := a.current()
	if p == nil {
		return nil, errors.New("no project open")
	}
	if err := a.store.SortVideosByDate(a.ctx, p.ID); err != nil {
		return nil, err
	}
	return a.view()
}

// SetVideoOrder applies a manual file order.
func (a *App) SetVideoOrder(ids []int64) (*ProjectView, error) {
	if err := a.store.SetVideoOrder(a.ctx, ids); err != nil {
		return nil, err
	}
	return a.view()
}

// RemoveVideo drops a file (and its segments) from the project. Cached API
// answers are kept, so re-adding it later is free.
func (a *App) RemoveVideo(id int64) (*ProjectView, error) {
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if running {
		return nil, errors.New("дождитесь окончания текущей задачи")
	}
	if err := a.store.DeleteVideo(a.ctx, id); err != nil {
		return nil, err
	}
	return a.view()
}

// ---- pipeline ----

func (a *App) runner(client llm.Client, mock bool) *pipeline.Runner {
	p := a.current()
	if p == nil {
		return nil
	}
	opts := pipeline.DefaultOptions()
	a.mu.Lock()
	opts.Lang = a.lang
	a.mu.Unlock()
	return &pipeline.Runner{Store: a.store, Bins: a.bins, Dirs: a.dirs, LLM: client, Project: p, Opts: opts, Log: a.log, Mock: mock,
		OnEvent: func(e pipeline.Event) { runtime.EventsEmit(a.ctx, "progress", e) }}
}

// RunResult is emitted on the "done" event.
type RunResult struct {
	Error   string  `json:"error"`
	CostUSD float64 `json:"cost_usd"`
	Hits    int     `json:"hits"`
	Elapsed string  `json:"elapsed"`
}

// Run executes the given stages (analyze, frames, describe, score) in the
// background. Progress arrives on the "progress" event, completion on "done".
func (a *App) Run(stages []string) error {
	if a.binsE != nil {
		return a.binsE
	}
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return errors.New("a run is already in progress")
	}
	if a.project == nil {
		a.mu.Unlock()
		return errors.New("no project open")
	}
	needsKey := false
	for _, s := range stages {
		if s == "describe" || s == "score" {
			needsKey = true
		}
	}
	if needsKey && os.Getenv("ANTHROPIC_API_KEY") == "" {
		a.mu.Unlock()
		return errors.New("ANTHROPIC_API_KEY is not set: add it in Settings or in the environment")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.running, a.cancel = true, cancel
	model := a.model
	a.mu.Unlock()

	r := a.runner(llm.NewAnthropic(model), false)
	go func() {
		start := time.Now()
		var err error
		for _, s := range stages {
			switch s {
			case "analyze":
				err = r.Analyze(ctx)
			case "frames":
				err = r.Frames(ctx)
			case "describe":
				err = r.Describe(ctx)
			case "score":
				err = r.Score(ctx)
			default:
				err = fmt.Errorf("unknown stage %q", s)
			}
			if err != nil {
				break
			}
		}
		cost, hits := r.SessionCost()
		res := RunResult{CostUSD: cost, Hits: hits, Elapsed: time.Since(start).Round(time.Second).String()}
		if err != nil {
			if ctx.Err() != nil {
				res.Error = "cancelled; progress is saved"
			} else {
				res.Error = err.Error()
			}
		}
		a.mu.Lock()
		a.running, a.cancel = false, nil
		a.mu.Unlock()
		runtime.EventsEmit(a.ctx, "done", res)
	}()
	return nil
}

func (a *App) Cancel() {
	a.mu.Lock()
	c := a.cancel
	a.mu.Unlock()
	if c != nil {
		c()
	}
}

// ---- segment edits ----

func (a *App) SetManualScore(id int64, score int) error {
	var p *int
	if score >= 1 && score <= 10 {
		p = &score
	}
	return a.store.SetManualScore(a.ctx, id, p)
}

// SetSelected: 1 include, 0 exclude, -1 follow threshold.
func (a *App) SetSelected(id int64, sel int) error {
	var p *bool
	switch sel {
	case 1:
		t := true
		p = &t
	case 0:
		f := false
		p = &f
	}
	return a.store.SetSelected(a.ctx, id, p)
}

// ResetScores discards model scores so scoring can run again (e.g. with a
// different model). Manual scores are kept.
func (a *App) ResetScores() error {
	p := a.current()
	if p == nil {
		return errors.New("no project open")
	}
	return a.store.ResetScores(a.ctx, p.ID)
}

// ---- preview ----

// ProxyURL renders (once) and returns the preview clip URL for a segment.
func (a *App) ProxyURL(id int64) (string, error) {
	if a.binsE != nil {
		return "", a.binsE
	}
	s, err := a.store.SegmentByID(a.ctx, id)
	if err != nil {
		return "", err
	}
	v, err := a.store.VideoByID(a.ctx, s.VideoID)
	if err != nil {
		return "", err
	}
	rel, err := proxy.Ensure(a.ctx, a.bins, a.dirs.Proxies, v.Path, v.FileKey, s.Start, s.End, proxy.Options{})
	if err != nil {
		return "", err
	}
	return "/media/proxy/" + filepath.ToSlash(rel), nil
}

// mediaHandler serves cached frames and proxies to the WebView. Paths are
// confined to the two cache directories.
func (a *App) mediaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var root, rel string
		switch {
		case strings.HasPrefix(r.URL.Path, "/media/frame/"):
			root, rel = a.dirs.Frames, strings.TrimPrefix(r.URL.Path, "/media/frame/")
		case strings.HasPrefix(r.URL.Path, "/media/proxy/"):
			root, rel = a.dirs.Proxies, strings.TrimPrefix(r.URL.Path, "/media/proxy/")
		default:
			http.NotFound(w, r)
			return
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		full := filepath.Join(root, clean)
		f, err := os.Open(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeContent(w, r, st.Name(), st.ModTime(), f)
	})
}

// ---- export ----

type ExportOptions struct {
	Threshold int    `json:"threshold"`
	Out       string `json:"out"` // .mp4 for video; any base name when NoVideo
	Encode    bool   `json:"encode"`
	Height    int    `json:"height"`
	EDL       bool   `json:"edl"`
	XML       bool   `json:"xml"`
	NoVideo   bool   `json:"no_video"` // write only EDL/XML, skip the render
}

type PlanItem struct {
	Video string  `json:"video"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Label string  `json:"label"`
}

type PlanView struct {
	Clips    int        `json:"clips"`
	Duration float64    `json:"duration"`
	Items    []PlanItem `json:"items"`
}

func (a *App) ExportPlan(threshold int) (*PlanView, error) {
	r := a.runner(llm.NewMock(a.model), true)
	if r == nil {
		return nil, errors.New("no project open")
	}
	plan, err := r.BuildPlan(a.ctx, threshold)
	if err != nil {
		return nil, err
	}
	pv := &PlanView{Clips: len(plan.Clips), Duration: plan.TotalDuration(), Items: []PlanItem{}}
	for _, c := range plan.Clips {
		pv.Items = append(pv.Items, PlanItem{Video: filepath.Base(c.Path), Start: c.Start, End: c.End, Label: c.Label})
	}
	return pv, nil
}

// PickExportPath opens a save dialog; kind is "mp4" (video) or "edl" (lists only).
func (a *App) PickExportPath(defaultName, kind string) (string, error) {
	filters := []runtime.FileFilter{{DisplayName: "MP4 video", Pattern: "*.mp4"}}
	title := "Экспорт монтажа"
	if kind == "edl" {
		filters = []runtime.FileFilter{{DisplayName: "EDL / XML", Pattern: "*.edl;*.xml"}}
		title = "Сохранить EDL / XML"
	}
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:            title,
		DefaultDirectory: a.dirs.Exports,
		DefaultFilename:  defaultName,
		Filters:          filters,
	})
}

// Export renders in the background; progress on "progress" (stage "render"),
// completion on "export-done".
func (a *App) Export(opts ExportOptions) error {
	if a.binsE != nil {
		return a.binsE
	}
	if opts.Out == "" {
		return errors.New("no output path")
	}
	r := a.runner(llm.NewMock(a.model), true)
	if r == nil {
		return errors.New("no project open")
	}
	plan, err := r.BuildPlan(a.ctx, opts.Threshold)
	if err != nil {
		return err
	}
	if len(plan.Clips) == 0 {
		return errors.New("nothing selected: lower the threshold or include segments manually")
	}
	if opts.NoVideo && !opts.EDL && !opts.XML {
		return errors.New("выберите хотя бы один формат: EDL или XML")
	}
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return errors.New("a run is already in progress")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.running, a.cancel = true, cancel
	a.mu.Unlock()
	go func() {
		start := time.Now()
		base := strings.TrimSuffix(opts.Out, filepath.Ext(opts.Out))
		var err error
		if err = os.MkdirAll(filepath.Dir(opts.Out), 0o755); err == nil && opts.EDL {
			err = os.WriteFile(base+".edl", []byte(export.EDL(plan)), 0o644)
		}
		if err == nil && opts.XML {
			err = os.WriteFile(base+".xml", []byte(export.XMEML(plan)), 0o644)
		}
		if err == nil && !opts.NoVideo {
			mode := export.ModeCopy
			if opts.Encode {
				mode = export.ModeEncode
			}
			err = export.Render(ctx, a.bins, plan, opts.Out, export.RenderOptions{Mode: mode, Height: opts.Height}, func(done, total float64) {
				runtime.EventsEmit(a.ctx, "progress", pipeline.Event{Stage: "render", Done: int(done), Total: int(total)})
			})
		}
		res := RunResult{Elapsed: time.Since(start).Round(time.Second).String()}
		if err != nil {
			if ctx.Err() != nil {
				res.Error = "cancelled"
				os.Remove(opts.Out)
			} else {
				res.Error = err.Error()
			}
		}
		a.mu.Lock()
		a.running, a.cancel = false, nil
		a.mu.Unlock()
		runtime.EventsEmit(a.ctx, "export-done", res)
	}()
	return nil
}

func (a *App) ShowInFolder(path string) {
	if path == "" {
		return
	}
	runtime.BrowserOpenURL(a.ctx, "file://"+filepath.ToSlash(filepath.Dir(path)))
}

// ---- settings ----

func (a *App) GetSettings() SettingsView {
	a.mu.Lock()
	defer a.mu.Unlock()
	sv := SettingsView{DataDir: a.dirs.Root, DataDirSource: a.dirs.Source, DataDirEnv: appdirs.EnvOverride(), Model: a.model, Lang: a.lang, Running: a.running, Version: "0.1.0"}
	sv.ConfigPath, _ = appdirs.ConfigPath()
	if a.binsE != nil {
		sv.FFmpegError = a.binsE.Error()
	} else {
		sv.FFmpegPath = a.bins.FFmpeg
		sv.FFmpegVersion, _ = a.bins.Version(a.ctx)
	}
	if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
		sv.APIKeySet = true
		if len(k) > 12 {
			sv.APIKeyHint = k[:8] + "…" + k[len(k)-4:]
		}
		switch {
		case a.keyFromConfig:
			sv.APIKeySource = "config"
		case a.keySession:
			sv.APIKeySource = "session"
		default:
			sv.APIKeySource = "env"
		}
	}
	return sv
}

// DataDirSize returns the size of the data directory in bytes.
func (a *App) DataDirSize() int64 { return a.dirs.Size() }

// PickDataDir opens a folder chooser for the new data location.
func (a *App) PickDataDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Папка данных Longcut", DefaultDirectory: a.dirs.Root})
}

// SetDataDir relocates the data directory. When move is true the existing
// database and caches are moved there first (progress on "progress" with
// stage "move"). Completion arrives on "data-dir-done"; on success the
// current project is closed and "data-dir-changed" is emitted.
func (a *App) SetDataDir(path string, move bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("folder not chosen")
	}
	if appdirs.EnvOverride() {
		return errors.New("папка задана переменной окружения LONGCUT_DATA_DIR; уберите её, чтобы менять папку из приложения")
	}
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return errors.New("дождитесь окончания текущей задачи")
	}
	newDirs, err := appdirs.ResolveAt(path)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	if filepath.Clean(newDirs.Root) == filepath.Clean(a.dirs.Root) {
		a.mu.Unlock()
		return nil
	}
	if move && newDirs.HasData() {
		a.mu.Unlock()
		return errors.New("в выбранной папке уже есть данные Longcut: выберите пустую папку или отключите перенос")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.running, a.cancel = true, cancel
	old := a.dirs
	a.mu.Unlock()

	go func() {
		res := RunResult{}
		start := time.Now()
		if a.store != nil {
			a.store.Close()
			a.store = nil
		}
		var err error
		if move {
			err = appdirs.Move(old, newDirs, func(done, total int64) {
				runtime.EventsEmit(a.ctx, "progress", pipeline.Event{Stage: "move", Done: int(done >> 20), Total: int(total >> 20)})
			})
		}
		target := newDirs
		if err != nil {
			// Stay where we were.
			target = old
			res.Error = "перенос не удался: " + err.Error()
		} else if serr := appdirs.SaveConfig(appdirs.Config{DataDir: newDirs.Root}); serr != nil {
			target = old
			res.Error = "не удалось сохранить настройку: " + serr.Error()
		} else {
			target.Source = "config"
		}
		st, oerr := store.Open(target.DBPath)
		if oerr != nil {
			res.Error = "не удалось открыть базу: " + oerr.Error()
		}
		a.mu.Lock()
		a.dirs = target
		a.store = st
		a.project = nil
		a.bins, a.binsE = ffmpeg.Find(target.Bin)
		a.running, a.cancel = false, nil
		a.mu.Unlock()
		res.Elapsed = time.Since(start).Round(time.Second).String()
		_ = ctx
		runtime.EventsEmit(a.ctx, "data-dir-done", res)
		if res.Error == "" {
			runtime.EventsEmit(a.ctx, "data-dir-changed", target.Root)
		}
	}()
	return nil
}

// OpenDataDir shows the data folder in the OS file manager.
func (a *App) OpenDataDir() {
	runtime.BrowserOpenURL(a.ctx, "file://"+filepath.ToSlash(a.dirs.Root))
}

// SetAPIKey sets the key for this process; with remember=true it is also
// stored in the app config (file mode 0600) and reused on the next launch.
// An empty key clears both.
func (a *App) SetAPIKey(key string, remember bool) error {
	key = strings.TrimSpace(key)
	cfg, _ := appdirs.LoadConfig()
	a.mu.Lock()
	defer a.mu.Unlock()
	if key == "" {
		os.Unsetenv("ANTHROPIC_API_KEY")
		a.keyFromConfig, a.keySession = false, false
		if cfg.APIKey != "" {
			cfg.APIKey = ""
			return appdirs.SaveConfig(cfg)
		}
		return nil
	}
	os.Setenv("ANTHROPIC_API_KEY", key)
	a.keyFromConfig, a.keySession = remember, !remember
	if remember {
		cfg.APIKey = key
	} else {
		cfg.APIKey = ""
	}
	return appdirs.SaveConfig(cfg)
}

func (a *App) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if m := strings.TrimSpace(model); m != "" {
		a.model = m
	}
}

func (a *App) SetLang(lang string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if l := strings.TrimSpace(lang); l != "" {
		a.lang = l
	}
}

// Models lists selectable models with pricing.
type ModelInfo struct {
	ID     string  `json:"id"`
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

func (a *App) Models() []ModelInfo {
	var out []ModelInfo
	for _, id := range []string{"claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-opus-5"} {
		p, _ := llm.PriceFor(id)
		out = append(out, ModelInfo{ID: id, Input: p.Input, Output: p.Output})
	}
	return out
}

// RecheckFFmpeg re-runs the binary lookup (after the user installed it).
func (a *App) RecheckFFmpeg() SettingsView {
	a.bins, a.binsE = ffmpeg.Find(a.dirs.Bin)
	return a.GetSettings()
}

// DownloadFFmpeg fetches a static build into the app bin dir. Progress is
// reported on "progress" with stage "ffmpeg".
func (a *App) DownloadFFmpeg() error {
	go func() {
		err := ffmpeg.Download(a.ctx, a.dirs.Bin, func(done, total int64) {
			runtime.EventsEmit(a.ctx, "progress", pipeline.Event{Stage: "ffmpeg", Done: int(done >> 20), Total: int(total >> 20)})
		})
		res := RunResult{}
		if err != nil {
			res.Error = err.Error()
		} else {
			a.bins, a.binsE = ffmpeg.Find(a.dirs.Bin)
		}
		runtime.EventsEmit(a.ctx, "ffmpeg-done", res)
	}()
	return nil
}
