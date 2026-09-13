// Package store persists projects, videos, segments, API cache and usage in a
// single SQLite database (pure Go driver, no CGO).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Segment status progression. Each pipeline stage advances the status; a
// crashed run resumes by selecting segments whose status is below the target.
const (
	StatusDetected  = "detected"
	StatusFramed    = "framed"
	StatusDescribed = "described"
	StatusScored    = "scored"
)

var statusRank = map[string]int{StatusDetected: 0, StatusFramed: 1, StatusDescribed: 2, StatusScored: 3}

// StatusRank orders statuses for "at least" queries.
func StatusRank(s string) int { return statusRank[s] }

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS projects (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  game       TEXT NOT NULL DEFAULT '',
  settings   TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS videos (
  id          INTEGER PRIMARY KEY,
  project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  path        TEXT NOT NULL,
  file_key    TEXT NOT NULL,           -- quick content hash: size + head/tail sample
  duration    REAL NOT NULL,
  width       INTEGER NOT NULL,
  height      INTEGER NOT NULL,
  fps         REAL NOT NULL,
  size        INTEGER NOT NULL,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  recorded_at TEXT,                    -- RFC3339 guess of the recording start
  analyzed    INTEGER NOT NULL DEFAULT 0,
  analysis    BLOB,                    -- gzip(JSON scenes.Analysis)
  seg_params  TEXT,                    -- JSON scenes.Params used for current segments
  created_at  TEXT NOT NULL,
  UNIQUE(project_id, path)
);

CREATE TABLE IF NOT EXISTS segments (
  id           INTEGER PRIMARY KEY,
  video_id     INTEGER NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  idx          INTEGER NOT NULL,
  start_sec    REAL NOT NULL,
  end_sec      REAL NOT NULL,
  motion_avg   REAL NOT NULL DEFAULT 0,
  motion_max   REAL NOT NULL DEFAULT 0,
  luma_avg     REAL NOT NULL DEFAULT 0,
  cut_score    REAL NOT NULL DEFAULT 0,
  status       TEXT NOT NULL DEFAULT 'detected',
  frames       TEXT,                   -- JSON []frames.Frame
  description  TEXT,
  desc_model   TEXT,
  score        INTEGER,                -- model score 1..10
  score_reason TEXT,
  score_model  TEXT,
  score_manual INTEGER,                -- user override
  selected     INTEGER,                -- explicit include/exclude override (NULL = follow threshold)
  error        TEXT,
  updated_at   TEXT NOT NULL,
  UNIQUE(video_id, idx)
);
CREATE INDEX IF NOT EXISTS segments_status ON segments(video_id, status);

-- Cached LLM responses keyed by a hash of everything that influences the
-- answer (model, prompt version, frame hashes / context text).
CREATE TABLE IF NOT EXISTS api_cache (
  key            TEXT PRIMARY KEY,
  kind           TEXT NOT NULL,        -- describe | score
  model          TEXT NOT NULL,
  response       TEXT NOT NULL,
  input_tokens   INTEGER NOT NULL,
  output_tokens  INTEGER NOT NULL,
  cache_read     INTEGER NOT NULL DEFAULT 0,
  cache_write    INTEGER NOT NULL DEFAULT 0,
  cost_usd       REAL NOT NULL,
  created_at     TEXT NOT NULL
);

-- Actual spend per project (only real API calls, not cache hits).
CREATE TABLE IF NOT EXISTS usage (
  id            INTEGER PRIMARY KEY,
  project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL,
  model         TEXT NOT NULL,
  input_tokens  INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  cache_read    INTEGER NOT NULL DEFAULT 0,
  cache_write   INTEGER NOT NULL DEFAULT 0,
  cost_usd      REAL NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS usage_project ON usage(project_id);
`

// Store wraps the database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// modernc sqlite is safe with one writer; keep a single connection to
	// avoid SQLITE_BUSY between the worker goroutines.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Additive migrations for databases created by earlier versions.
	for _, stmt := range []string{
		"ALTER TABLE videos ADD COLUMN recorded_at TEXT",
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// Close the database.
func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ---- projects ----

type Project struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Game      string    `json:"game"`
	Settings  string    `json:"settings"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetOrCreateProject returns the project with that name, creating it if needed.
func (s *Store) GetOrCreateProject(ctx context.Context, name, game string) (*Project, error) {
	p, err := s.ProjectByName(ctx, name)
	if err == nil {
		if game != "" && game != p.Game {
			if _, err := s.db.ExecContext(ctx, `UPDATE projects SET game=?, updated_at=? WHERE id=?`, game, now(), p.ID); err != nil {
				return nil, err
			}
			p.Game = game
		}
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO projects(name, game, created_at, updated_at) VALUES(?,?,?,?)`, name, game, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.ProjectByID(ctx, id)
}

func (s *Store) ProjectByName(ctx context.Context, name string) (*Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT id,name,game,settings,created_at,updated_at FROM projects WHERE name=?`, name))
}

func (s *Store) ProjectByID(ctx context.Context, id int64) (*Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT id,name,game,settings,created_at,updated_at FROM projects WHERE id=?`, id))
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,game,settings,created_at,updated_at FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanProject(r scanner) (*Project, error) {
	var p Project
	var c, u string
	if err := r.Scan(&p.ID, &p.Name, &p.Game, &p.Settings, &c, &u); err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
	return &p, nil
}

// ---- videos ----

type Video struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"project_id"`
	Path       string  `json:"path"`
	FileKey    string  `json:"file_key"`
	Duration   float64 `json:"duration"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        float64 `json:"fps"`
	Size       int64   `json:"size"`
	SortOrder  int     `json:"sort_order"`
	RecordedAt string  `json:"recorded_at"` // RFC3339 or ""
	Analyzed   bool    `json:"analyzed"`
	SegParams  string  `json:"seg_params"`
}

// UpsertVideo registers a video in a project; if it already exists (same
// path) its metadata is refreshed. Returns the row and whether it was new.
func (s *Store) UpsertVideo(ctx context.Context, v *Video) (bool, error) {
	existing, err := s.VideoByPath(ctx, v.ProjectID, v.Path)
	if err == nil {
		v.ID = existing.ID
		v.Analyzed = existing.Analyzed
		v.SegParams = existing.SegParams
		if v.RecordedAt != "" && existing.RecordedAt == "" {
			_, _ = s.db.ExecContext(ctx, `UPDATE videos SET recorded_at=? WHERE id=?`, v.RecordedAt, v.ID)
		}
		if existing.FileKey != v.FileKey {
			// File changed on disk: analysis and segments are stale.
			_, err := s.db.ExecContext(ctx, `UPDATE videos SET file_key=?, duration=?, width=?, height=?, fps=?, size=?, recorded_at=?, analyzed=0, analysis=NULL, seg_params=NULL WHERE id=?`,
				v.FileKey, v.Duration, v.Width, v.Height, v.FPS, v.Size, nullStr(v.RecordedAt), v.ID)
			if err != nil {
				return false, err
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM segments WHERE video_id=?`, v.ID); err != nil {
				return false, err
			}
			v.Analyzed = false
			v.SegParams = ""
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO videos(project_id,path,file_key,duration,width,height,fps,size,sort_order,recorded_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		v.ProjectID, v.Path, v.FileKey, v.Duration, v.Width, v.Height, v.FPS, v.Size, v.SortOrder, nullStr(v.RecordedAt), now())
	if err != nil {
		return false, err
	}
	v.ID, _ = res.LastInsertId()
	return true, nil
}

const videoCols = `id,project_id,path,file_key,duration,width,height,fps,size,sort_order,COALESCE(recorded_at,''),analyzed,COALESCE(seg_params,'')`

func scanVideo(r scanner) (*Video, error) {
	var v Video
	var analyzed int
	if err := r.Scan(&v.ID, &v.ProjectID, &v.Path, &v.FileKey, &v.Duration, &v.Width, &v.Height, &v.FPS, &v.Size, &v.SortOrder, &v.RecordedAt, &analyzed, &v.SegParams); err != nil {
		return nil, err
	}
	v.Analyzed = analyzed != 0
	return &v, nil
}

func (s *Store) VideoByPath(ctx context.Context, projectID int64, path string) (*Video, error) {
	return scanVideo(s.db.QueryRowContext(ctx, `SELECT `+videoCols+` FROM videos WHERE project_id=? AND path=?`, projectID, path))
}

func (s *Store) VideoByID(ctx context.Context, id int64) (*Video, error) {
	return scanVideo(s.db.QueryRowContext(ctx, `SELECT `+videoCols+` FROM videos WHERE id=?`, id))
}

func (s *Store) ListVideos(ctx context.Context, projectID int64) ([]Video, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+videoCols+` FROM videos WHERE project_id=? ORDER BY sort_order, recorded_at, path`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Video
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// SetRecordedAt stores a recording start guess for a video.
func (s *Store) SetRecordedAt(ctx context.Context, id int64, rfc3339 string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE videos SET recorded_at=? WHERE id=?`, nullStr(rfc3339), id)
	return err
}

// SetVideoOrder assigns sort_order following the given id sequence.
func (s *Store) SetVideoOrder(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE videos SET sort_order=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SortVideosByDate orders a project's videos by recording start (unknown
// dates last, then by path) and persists the result.
func (s *Store) SortVideosByDate(ctx context.Context, projectID int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM videos WHERE project_id=? ORDER BY CASE WHEN recorded_at IS NULL OR recorded_at='' THEN 1 ELSE 0 END, recorded_at, path`, projectID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	return s.SetVideoOrder(ctx, ids)
}

// DeleteVideo removes a video and its segments from the project.
func (s *Store) DeleteVideo(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM videos WHERE id=?`, id)
	return err
}

// SaveAnalysis stores the compressed analysis blob and marks the video analyzed.
func (s *Store) SaveAnalysis(ctx context.Context, videoID int64, blob []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE videos SET analysis=?, analyzed=1 WHERE id=?`, blob, videoID)
	return err
}

// LoadAnalysis returns the stored blob (nil if none).
func (s *Store) LoadAnalysis(ctx context.Context, videoID int64) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT analysis FROM videos WHERE id=?`, videoID).Scan(&blob)
	if err != nil {
		return nil, err
	}
	return blob, nil
}

// ---- segments ----

type Segment struct {
	ID          int64   `json:"id"`
	VideoID     int64   `json:"video_id"`
	Idx         int     `json:"idx"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	MotionAvg   float64 `json:"motion_avg"`
	MotionMax   float64 `json:"motion_max"`
	LumaAvg     float64 `json:"luma_avg"`
	CutScore    float64 `json:"cut_score"`
	Status      string  `json:"status"`
	FramesJSON  string  `json:"frames_json,omitempty"`
	Description string  `json:"description"`
	DescModel   string  `json:"desc_model"`
	Score       *int    `json:"score"`
	ScoreReason string  `json:"score_reason"`
	ScoreModel  string  `json:"score_model"`
	ScoreManual *int    `json:"score_manual"`
	Selected    *bool   `json:"selected"`
	Error       string  `json:"error"`
}

// EffectiveScore is the manual override if set, else the model score, else 0.
func (sg *Segment) EffectiveScore() int {
	if sg.ScoreManual != nil {
		return *sg.ScoreManual
	}
	if sg.Score != nil {
		return *sg.Score
	}
	return 0
}

// ReplaceSegments deletes existing segments of a video and inserts new ones,
// preserving descriptions/scores for segments whose boundaries did not move
// (so re-segmenting with the same parameters is free).
func (s *Store) ReplaceSegments(ctx context.Context, videoID int64, segs []Segment, params string) error {
	old, err := s.ListSegments(ctx, videoID)
	if err != nil {
		return err
	}
	type key struct{ a, b int64 }
	oldBy := map[key]Segment{}
	for _, o := range old {
		oldBy[key{msRound(o.Start), msRound(o.End)}] = o
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE video_id=?`, videoID); err != nil {
		return err
	}
	t := now()
	for i := range segs {
		sg := &segs[i]
		sg.VideoID = videoID
		sg.Idx = i
		if o, ok := oldBy[key{msRound(sg.Start), msRound(sg.End)}]; ok {
			sg.Status, sg.FramesJSON, sg.Description, sg.DescModel = o.Status, o.FramesJSON, o.Description, o.DescModel
			sg.Score, sg.ScoreReason, sg.ScoreModel, sg.ScoreManual, sg.Selected = o.Score, o.ScoreReason, o.ScoreModel, o.ScoreManual, o.Selected
		}
		if sg.Status == "" {
			sg.Status = StatusDetected
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO segments(video_id,idx,start_sec,end_sec,motion_avg,motion_max,luma_avg,cut_score,status,frames,description,desc_model,score,score_reason,score_model,score_manual,selected,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			videoID, i, sg.Start, sg.End, sg.MotionAvg, sg.MotionMax, sg.LumaAvg, sg.CutScore, sg.Status,
			nullStr(sg.FramesJSON), nullStr(sg.Description), nullStr(sg.DescModel), nullInt(sg.Score), nullStr(sg.ScoreReason), nullStr(sg.ScoreModel), nullInt(sg.ScoreManual), nullBool(sg.Selected), t)
		if err != nil {
			return err
		}
		sg.ID, _ = res.LastInsertId()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE videos SET seg_params=? WHERE id=?`, params, videoID); err != nil {
		return err
	}
	return tx.Commit()
}

func msRound(f float64) int64 { return int64(f*1000 + 0.5) }

const segCols = `id,video_id,idx,start_sec,end_sec,motion_avg,motion_max,luma_avg,cut_score,status,
	COALESCE(frames,''),COALESCE(description,''),COALESCE(desc_model,''),score,COALESCE(score_reason,''),COALESCE(score_model,''),score_manual,selected,COALESCE(error,'')`

func scanSegment(r scanner) (*Segment, error) {
	var sg Segment
	var score, manual sql.NullInt64
	var selected sql.NullInt64
	if err := r.Scan(&sg.ID, &sg.VideoID, &sg.Idx, &sg.Start, &sg.End, &sg.MotionAvg, &sg.MotionMax, &sg.LumaAvg, &sg.CutScore, &sg.Status,
		&sg.FramesJSON, &sg.Description, &sg.DescModel, &score, &sg.ScoreReason, &sg.ScoreModel, &manual, &selected, &sg.Error); err != nil {
		return nil, err
	}
	if score.Valid {
		v := int(score.Int64)
		sg.Score = &v
	}
	if manual.Valid {
		v := int(manual.Int64)
		sg.ScoreManual = &v
	}
	if selected.Valid {
		v := selected.Int64 != 0
		sg.Selected = &v
	}
	return &sg, nil
}

func (s *Store) ListSegments(ctx context.Context, videoID int64) ([]Segment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+segCols+` FROM segments WHERE video_id=? ORDER BY idx`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Segment
	for rows.Next() {
		sg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sg)
	}
	return out, rows.Err()
}

// ListProjectSegments returns all segments of a project in timeline order.
func (s *Store) ListProjectSegments(ctx context.Context, projectID int64) ([]Segment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+segColsQualified+` FROM segments s JOIN videos v ON v.id=s.video_id WHERE v.project_id=? ORDER BY v.sort_order, v.path, s.idx`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Segment
	for rows.Next() {
		sg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sg)
	}
	return out, rows.Err()
}

const segColsQualified = `s.id,s.video_id,s.idx,s.start_sec,s.end_sec,s.motion_avg,s.motion_max,s.luma_avg,s.cut_score,s.status,
	COALESCE(s.frames,''),COALESCE(s.description,''),COALESCE(s.desc_model,''),s.score,COALESCE(s.score_reason,''),COALESCE(s.score_model,''),s.score_manual,s.selected,COALESCE(s.error,'')`

func (s *Store) SegmentByID(ctx context.Context, id int64) (*Segment, error) {
	return scanSegment(s.db.QueryRowContext(ctx, `SELECT `+segCols+` FROM segments WHERE id=?`, id))
}

// SetFrames records extracted frames and advances to framed.
func (s *Store) SetFrames(ctx context.Context, id int64, framesJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET frames=?, status=CASE WHEN status='detected' THEN 'framed' ELSE status END, error=NULL, updated_at=? WHERE id=?`, framesJSON, now(), id)
	return err
}

// SetDescription records the description and advances to described.
func (s *Store) SetDescription(ctx context.Context, id int64, desc, model string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET description=?, desc_model=?, status=CASE WHEN status IN ('detected','framed') THEN 'described' ELSE status END, error=NULL, updated_at=? WHERE id=?`, desc, model, now(), id)
	return err
}

// SetScore records the model score and advances to scored.
func (s *Store) SetScore(ctx context.Context, id int64, score int, reason, model string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET score=?, score_reason=?, score_model=?, status='scored', error=NULL, updated_at=? WHERE id=?`, score, reason, model, now(), id)
	return err
}

// SetManualScore sets or clears (nil) the user's override.
func (s *Store) SetManualScore(ctx context.Context, id int64, score *int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET score_manual=?, updated_at=? WHERE id=?`, nullInt(score), now(), id)
	return err
}

// SetSelected sets or clears (nil) the explicit include/exclude override.
func (s *Store) SetSelected(ctx context.Context, id int64, sel *bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET selected=?, updated_at=? WHERE id=?`, nullBool(sel), now(), id)
	return err
}

// SetError records a per-segment failure without changing status.
func (s *Store) SetError(ctx context.Context, id int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET error=?, updated_at=? WHERE id=?`, msg, now(), id)
	return err
}

// ResetScores clears model scores in a project so scoring can be re-run
// (e.g. after a prompt change). Manual overrides are kept.
func (s *Store) ResetScores(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET score=NULL, score_reason=NULL, score_model=NULL, status='described', updated_at=? WHERE video_id IN (SELECT id FROM videos WHERE project_id=?) AND status='scored'`, now(), projectID)
	return err
}

// ResetMock rolls back segments processed by the offline mock so a real run
// redoes them: mock descriptions go back to framed, mock scores to described.
func (s *Store) ResetMock(ctx context.Context, projectID int64) error {
	t := now()
	if _, err := s.db.ExecContext(ctx, `UPDATE segments SET score=NULL, score_reason=NULL, score_model=NULL, status='described', updated_at=?
		WHERE video_id IN (SELECT id FROM videos WHERE project_id=?) AND score_model='mock'`, t, projectID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE segments SET description=NULL, desc_model=NULL, score=NULL, score_reason=NULL, score_model=NULL, status='framed', updated_at=?
		WHERE video_id IN (SELECT id FROM videos WHERE project_id=?) AND desc_model='mock'`, t, projectID)
	return err
}

// ---- api cache ----

type CacheEntry struct {
	Key          string
	Kind         string
	Model        string
	Response     string
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	CostUSD      float64
}

func (s *Store) CacheGet(ctx context.Context, key string) (*CacheEntry, error) {
	var e CacheEntry
	err := s.db.QueryRowContext(ctx, `SELECT key,kind,model,response,input_tokens,output_tokens,cache_read,cache_write,cost_usd FROM api_cache WHERE key=?`, key).
		Scan(&e.Key, &e.Kind, &e.Model, &e.Response, &e.InputTokens, &e.OutputTokens, &e.CacheRead, &e.CacheWrite, &e.CostUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// CachePut stores a response and records the spend against the project.
func (s *Store) CachePut(ctx context.Context, projectID int64, e CacheEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t := now()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO api_cache(key,kind,model,response,input_tokens,output_tokens,cache_read,cache_write,cost_usd,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		e.Key, e.Kind, e.Model, e.Response, e.InputTokens, e.OutputTokens, e.CacheRead, e.CacheWrite, e.CostUSD, t); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage(project_id,kind,model,input_tokens,output_tokens,cache_read,cache_write,cost_usd,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		projectID, e.Kind, e.Model, e.InputTokens, e.OutputTokens, e.CacheRead, e.CacheWrite, e.CostUSD, t); err != nil {
		return err
	}
	return tx.Commit()
}

// Usage is aggregated spend.
type Usage struct {
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CacheRead    int64   `json:"cache_read"`
	CostUSD      float64 `json:"cost_usd"`
}

// ProjectUsage sums spend per kind for a project ("" key = total).
func (s *Store) ProjectUsage(ctx context.Context, projectID int64) (map[string]Usage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cache_read), SUM(cost_usd) FROM usage WHERE project_id=? GROUP BY kind`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Usage{}
	var total Usage
	for rows.Next() {
		var kind string
		var u Usage
		if err := rows.Scan(&kind, &u.Calls, &u.InputTokens, &u.OutputTokens, &u.CacheRead, &u.CostUSD); err != nil {
			return nil, err
		}
		out[kind] = u
		total.Calls += u.Calls
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
		total.CacheRead += u.CacheRead
		total.CostUSD += u.CostUSD
	}
	out[""] = total
	return out, rows.Err()
}

// Counts returns segment counts per status for a project.
func (s *Store) Counts(ctx context.Context, projectID int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.status, COUNT(*) FROM segments s JOIN videos v ON v.id=s.video_id WHERE v.project_id=? GROUP BY s.status`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ---- helpers ----

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullBool(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return 1
	}
	return 0
}

// MarshalJSON helper for storing structs.
func JSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
