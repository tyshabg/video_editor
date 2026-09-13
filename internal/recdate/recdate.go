// Package recdate guesses when a recording started, so files can be laid out
// chronologically regardless of the order they were added in.
package recdate

import (
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// Filename patterns, most specific first:
//
//	"kenshi 2026.08.31 - 19.36.55.01.mp4"      NVIDIA ShadowPlay
//	"2026-08-31 19-36-55.mkv"                  OBS default
//	"Kenshi 2026-08-31 19-36-55.mp4"           Xbox Game Bar
//	"Replay 2026-08-31 19-36-55.mp4"           Medal / others
//	"20260831_193655.mp4", "20260831-193655"   cameras, some capture tools
var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(\d{4})[.\-_](\d{2})[.\-_](\d{2})\D{0,3}(\d{2})[.\-_:](\d{2})[.\-_:](\d{2})`),
	regexp.MustCompile(`(20\d{2})(\d{2})(\d{2})[_\-T ]?(\d{2})(\d{2})(\d{2})`),
}

// FromName extracts a start time from the file name, in local time.
func FromName(path string) (time.Time, bool) {
	base := filepath.Base(path)
	for _, re := range patterns {
		m := re.FindStringSubmatch(base)
		if m == nil {
			continue
		}
		n := make([]int, 6)
		for i := range n {
			n[i], _ = strconv.Atoi(m[i+1])
		}
		if n[0] < 2000 || n[0] > 2100 || n[1] < 1 || n[1] > 12 || n[2] < 1 || n[2] > 31 || n[3] > 23 || n[4] > 59 || n[5] > 59 {
			continue
		}
		t := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], n[5], 0, time.Local)
		// Reject impossible dates such as Feb 30 (time.Date normalises them).
		if t.Day() != n[2] {
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// Guess returns the best available start time and where it came from:
// "name" (parsed from the file name), "meta" (container creation_time) or
// "mtime" (file modification time minus duration, i.e. recording end minus
// length).
func Guess(path string, creationTime time.Time, mtime time.Time, duration time.Duration) (time.Time, string) {
	if t, ok := FromName(path); ok {
		return t, "name"
	}
	if !creationTime.IsZero() && creationTime.Year() > 2000 {
		return creationTime.Local(), "meta"
	}
	return mtime.Add(-duration), "mtime"
}
