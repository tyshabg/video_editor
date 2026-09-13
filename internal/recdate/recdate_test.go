package recdate

import (
	"testing"
	"time"
)

func TestFromName(t *testing.T) {
	cases := map[string]time.Time{
		`D:\Videosos\kenshi\kenshi 2026.08.31 - 19.36.55.01.mp4`: time.Date(2026, 8, 31, 19, 36, 55, 0, time.Local),
		`2026-08-31 19-36-55.mkv`:                                time.Date(2026, 8, 31, 19, 36, 55, 0, time.Local),
		`Kenshi 2026-08-31 19-36-55.mp4`:                         time.Date(2026, 8, 31, 19, 36, 55, 0, time.Local),
		`Replay_2026-01-02_03-04-05.mp4`:                         time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local),
		`20260831_193655.mp4`:                                    time.Date(2026, 8, 31, 19, 36, 55, 0, time.Local),
		`clip-20260831-193655-x.mp4`:                             time.Date(2026, 8, 31, 19, 36, 55, 0, time.Local),
	}
	for name, want := range cases {
		got, ok := FromName(name)
		if !ok || !got.Equal(want) {
			t.Errorf("%s: got %v ok=%v, want %v", name, got, ok, want)
		}
	}
	for _, bad := range []string{"kenshi.mp4", "session 12.34.56.mp4", "2026-13-01 10-00-00.mp4", "2026-02-30 10-00-00.mp4", "1999.01.01 - 10.00.00.mp4"} {
		if _, ok := FromName(bad); ok {
			t.Errorf("%s should not parse", bad)
		}
	}
}

func TestGuessFallbacks(t *testing.T) {
	meta := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	mt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.Local)
	if got, src := Guess("x.mp4", meta, mt, time.Hour); src != "meta" || !got.Equal(meta) {
		t.Errorf("meta fallback: %v %s", got, src)
	}
	if got, src := Guess("x.mp4", time.Time{}, mt, time.Hour); src != "mtime" || !got.Equal(mt.Add(-time.Hour)) {
		t.Errorf("mtime fallback: %v %s", got, src)
	}
	if _, src := Guess("2026-08-31 19-36-55.mkv", meta, mt, time.Hour); src != "name" {
		t.Errorf("name should win, got %s", src)
	}
}
