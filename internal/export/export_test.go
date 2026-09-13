package export

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"longcut/internal/appdirs"
	"longcut/internal/ffmpeg"
)

func samplePlan() Plan {
	return Plan{Title: "Kenshi ep1", Clips: []Clip{
		{Path: `K:\Videos\a b.mp4`, Start: 12.5, End: 40, FPS: 60, Width: 2560, Height: 1440, Duration: 600, Label: "#1 score 8",
			Notes: []string{"#1 score 8: Boss fight in the\nruined tower", "#2 score 7: Loot & escape"}},
		{Path: `K:\Videos\a b.mp4`, Start: 100, End: 130.25, FPS: 60, Width: 2560, Height: 1440, Duration: 600},
		{Path: `K:\Videos\c.mp4`, Start: 0, End: 5, FPS: 60, Width: 2560, Height: 1440, Duration: 60},
	}}
}

func TestTimecode(t *testing.T) {
	cases := map[float64]string{0: "00:00:00:00", 12.5: "00:00:12:30", 3661.0: "01:01:01:00", 0.999: "00:00:01:00"}
	for sec, want := range cases {
		if got := timecode(sec, 60); got != want {
			t.Errorf("timecode(%v)=%s want %s", sec, got, want)
		}
	}
	if got := timecode(1, 29.97); got != "00:00:00:30" && got != "00:00:01:00" {
		t.Errorf("29.97 timecode = %s", got)
	}
}

func TestEDL(t *testing.T) {
	edl := EDL(samplePlan())
	lines := strings.Split(strings.TrimSpace(edl), "\n")
	if lines[0] != "TITLE: Kenshi ep1" || lines[1] != "FCM: NON-DROP FRAME" {
		t.Fatalf("header: %q %q", lines[0], lines[1])
	}
	want := "001  AX       AA/V  C        00:00:12:30 00:00:40:00 00:00:00:00 00:00:27:30"
	if !strings.Contains(edl, want) {
		t.Fatalf("missing event line %q in\n%s", want, edl)
	}
	// record in of event 2 continues where event 1 ended
	if !strings.Contains(edl, "002  AX       AA/V  C        00:01:40:00 00:02:10:15 00:00:27:30 00:00:57:45") {
		t.Fatalf("event 2 wrong:\n%s", edl)
	}
	if !strings.Contains(edl, "* FROM CLIP NAME: a b.mp4") {
		t.Fatalf("clip name missing:\n%s", edl)
	}
	// Every merged segment gets its own full, single-line comment.
	if !strings.Contains(edl, "* COMMENT: #1 score 8: Boss fight in the ruined tower\n* COMMENT: #2 score 7: Loot & escape\n") {
		t.Fatalf("full comments missing:\n%s", edl)
	}
}

func TestXMEMLWellFormed(t *testing.T) {
	out := XMEML(samplePlan())
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("xml not well-formed: %v\n%s", err, out)
		}
	}
	for _, s := range []string{
		"<timebase>60</timebase>", "<ntsc>FALSE</ntsc>",
		"<start>0</start><end>1650</end><in>750</in><out>2400</out>",
		"<start>1650</start><end>3465</end><in>6000</in><out>7815</out>",
		"<pathurl>file://localhost/K:/Videos/a%20b.mp4</pathurl>",
		`<file id="file-1"/>`, `<file id="file-2">`,
		"<width>2560</width><height>1440</height>",
		"<mastercomment1>#1 score 8: Boss fight in the\nruined tower | #2 score 7: Loot &amp; escape</mastercomment1>",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q", s)
		}
	}
	if strings.Count(out, `<file id="file-1">`) != 1 {
		t.Errorf("file-1 should be fully defined exactly once")
	}
}

func TestRateOf(t *testing.T) {
	if tb, n := rateOf(59.94); tb != 60 || n != "TRUE" {
		t.Errorf("59.94 -> %d %s", tb, n)
	}
	if tb, n := rateOf(30); tb != 30 || n != "FALSE" {
		t.Errorf("30 -> %d %s", tb, n)
	}
}

func TestRenderCopyAndEncode(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	binDir := ""
	if d, err := appdirs.Resolve(); err == nil {
		binDir = d.Bin
	}
	bins, err := ffmpeg.Find(binDir)
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	// 12 s test clip with audio tone, 1 s GOP.
	if err := bins.Run(ctx, []string{"-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d=12", "-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-c:v", "libx264", "-preset", "veryfast", "-g", "30", "-keyint_min", "30", "-c:a", "aac", "-shortest", src}, nil); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Title: "t", Clips: []Clip{
		{Path: src, Start: 2, End: 5, FPS: 30, Width: 320, Height: 180, Duration: 12},
		{Path: src, Start: 8, End: 11, FPS: 30, Width: 320, Height: 180, Duration: 12},
	}}
	for _, mode := range []RenderMode{ModeCopy, ModeEncode} {
		out := filepath.Join(dir, string(mode)+".mp4")
		var last float64
		if err := Render(ctx, bins, plan, out, RenderOptions{Mode: mode, Preset: "veryfast"}, func(d, tot float64) { last = d }); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		info, err := bins.Probe(ctx, out)
		if err != nil {
			t.Fatal(err)
		}
		if info.Duration < 5.5 || info.Duration > 6.6 {
			t.Errorf("%s: duration %.2f, want ~6", mode, info.Duration)
		}
		if !info.HasAudio {
			t.Errorf("%s: audio lost", mode)
		}
		if last <= 0 {
			t.Errorf("%s: no progress reported", mode)
		}
		if st, _ := os.Stat(out); st == nil || st.Size() == 0 {
			t.Errorf("%s: empty output", mode)
		}
	}
}
