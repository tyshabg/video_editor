package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReplaceSegmentsPreservesWork(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p, err := s.GetOrCreateProject(ctx, "p", "Kenshi")
	if err != nil {
		t.Fatal(err)
	}
	v := Video{ProjectID: p.ID, Path: "x.mp4", FileKey: "k1", Duration: 100}
	if _, err := s.UpsertVideo(ctx, &v); err != nil {
		t.Fatal(err)
	}
	segs := []Segment{{Start: 0, End: 30}, {Start: 30, End: 70}, {Start: 70, End: 100}}
	if err := s.ReplaceSegments(ctx, v.ID, segs, "p1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListSegments(ctx, v.ID)
	if len(got) != 3 || got[1].Status != StatusDetected {
		t.Fatalf("unexpected %+v", got)
	}
	if err := s.SetFrames(ctx, got[1].ID, `[{"t":50}]`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDescription(ctx, got[1].ID, "fight", "m"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetScore(ctx, got[1].ID, 8, "boss", "m"); err != nil {
		t.Fatal(err)
	}
	seven := 7
	if err := s.SetManualScore(ctx, got[1].ID, &seven); err != nil {
		t.Fatal(err)
	}

	// Re-segment: middle boundary unchanged, last one moved.
	segs2 := []Segment{{Start: 0, End: 30}, {Start: 30, End: 70}, {Start: 70, End: 90}, {Start: 90, End: 100}}
	if err := s.ReplaceSegments(ctx, v.ID, segs2, "p2"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListSegments(ctx, v.ID)
	if len(got) != 4 {
		t.Fatalf("want 4 segments, got %d", len(got))
	}
	m := got[1]
	if m.Status != StatusScored || m.Description != "fight" || m.Score == nil || *m.Score != 8 || m.ScoreManual == nil || *m.ScoreManual != 7 || m.EffectiveScore() != 7 {
		t.Fatalf("work on unchanged segment lost: %+v", m)
	}
	if got[2].Status != StatusDetected || got[3].Status != StatusDetected {
		t.Fatalf("new segments should be detected: %+v %+v", got[2], got[3])
	}
	counts, _ := s.Counts(ctx, p.ID)
	if counts[StatusScored] != 1 || counts[StatusDetected] != 3 {
		t.Fatalf("counts %+v", counts)
	}
}

func TestUpsertVideoDetectsChangedFile(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p, _ := s.GetOrCreateProject(ctx, "p", "")
	v := Video{ProjectID: p.ID, Path: "x.mp4", FileKey: "k1", Duration: 100}
	if _, err := s.UpsertVideo(ctx, &v); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAnalysis(ctx, v.ID, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSegments(ctx, v.ID, []Segment{{Start: 0, End: 100}}, "p"); err != nil {
		t.Fatal(err)
	}
	same := Video{ProjectID: p.ID, Path: "x.mp4", FileKey: "k1", Duration: 100}
	isNew, err := s.UpsertVideo(ctx, &same)
	if err != nil || isNew || !same.Analyzed {
		t.Fatalf("same file: new=%v analyzed=%v err=%v", isNew, same.Analyzed, err)
	}
	changed := Video{ProjectID: p.ID, Path: "x.mp4", FileKey: "k2", Duration: 100}
	if _, err := s.UpsertVideo(ctx, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Analyzed {
		t.Fatal("changed file should require re-analysis")
	}
	segs, _ := s.ListSegments(ctx, changed.ID)
	if len(segs) != 0 {
		t.Fatal("segments of changed file should be dropped")
	}
}

func TestCacheAndUsage(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p, _ := s.GetOrCreateProject(ctx, "p", "")
	if e, err := s.CacheGet(ctx, "k"); err != nil || e != nil {
		t.Fatalf("miss expected: %v %v", e, err)
	}
	if err := s.CachePut(ctx, p.ID, CacheEntry{Key: "k", Kind: "describe", Model: "m", Response: `{"text":"x"}`, InputTokens: 100, OutputTokens: 10, CostUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := s.CachePut(ctx, p.ID, CacheEntry{Key: "k2", Kind: "score", Model: "m", Response: `{}`, InputTokens: 50, OutputTokens: 5, CostUSD: 0.25}); err != nil {
		t.Fatal(err)
	}
	e, err := s.CacheGet(ctx, "k")
	if err != nil || e == nil || e.Response != `{"text":"x"}` {
		t.Fatalf("hit expected: %+v %v", e, err)
	}
	u, _ := s.ProjectUsage(ctx, p.ID)
	if u[""].Calls != 2 || u[""].CostUSD != 0.75 || u["describe"].InputTokens != 100 {
		t.Fatalf("usage %+v", u)
	}
}

func TestResetMock(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p, _ := s.GetOrCreateProject(ctx, "p", "")
	v := Video{ProjectID: p.ID, Path: "x.mp4", FileKey: "k1", Duration: 100}
	_, _ = s.UpsertVideo(ctx, &v)
	_ = s.ReplaceSegments(ctx, v.ID, []Segment{{Start: 0, End: 50}, {Start: 50, End: 100}}, "p")
	segs, _ := s.ListSegments(ctx, v.ID)
	_ = s.SetFrames(ctx, segs[0].ID, "[]")
	_ = s.SetDescription(ctx, segs[0].ID, "[mock]", "mock")
	_ = s.SetScore(ctx, segs[0].ID, 5, "mock", "mock")
	_ = s.SetFrames(ctx, segs[1].ID, "[]")
	_ = s.SetDescription(ctx, segs[1].ID, "real", "claude")
	_ = s.SetScore(ctx, segs[1].ID, 5, "mock", "mock")
	if err := s.ResetMock(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	segs, _ = s.ListSegments(ctx, v.ID)
	if segs[0].Status != StatusFramed || segs[0].Description != "" {
		t.Fatalf("mock description should be reset: %+v", segs[0])
	}
	if segs[1].Status != StatusDescribed || segs[1].Description != "real" || segs[1].Score != nil {
		t.Fatalf("real description kept, mock score reset: %+v", segs[1])
	}
}
