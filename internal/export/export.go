// Package export turns selected segments into a rendered video (ffmpeg
// concat) and into edit decision lists for NLEs: CMX 3600 EDL and Final Cut
// Pro 7 XML (xmeml), which both Premiere Pro and DaVinci Resolve import.
package export

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"longcut/internal/ffmpeg"
)

// Clip is one selected range of a source file.
type Clip struct {
	Path     string  // absolute source path
	Start    float64 // seconds, inclusive
	End      float64 // seconds, exclusive
	FPS      float64 // source frame rate
	Width    int
	Height   int
	Duration float64 // full source duration
	Label    string  // short, for lists: "#12 score 8: first words…"
	// Notes hold the full text of every segment merged into this clip; they
	// go into the EDL as comments and into the XML as clip comments.
	Notes []string
}

func (c Clip) Len() float64 { return c.End - c.Start }

// Plan is an ordered list of clips plus totals.
type Plan struct {
	Title string
	Clips []Clip
}

// TotalDuration of the cut in seconds.
func (p Plan) TotalDuration() float64 {
	var d float64
	for _, c := range p.Clips {
		d += c.Len()
	}
	return d
}

// RenderMode selects how the video is produced.
type RenderMode string

const (
	// ModeCopy stream-copies: instant and lossless, but cuts land on the
	// nearest preceding keyframe (segments detected in keyframe mode already
	// sit on keyframes, so this is exact for them).
	ModeCopy RenderMode = "copy"
	// ModeEncode re-encodes with libx264/aac for frame-accurate cuts.
	ModeEncode RenderMode = "encode"
)

// RenderOptions controls the ffmpeg render.
type RenderOptions struct {
	Mode   RenderMode
	CRF    int    // encode mode; default 18
	Preset string // encode mode; default "fast"
	// Height downscales in encode mode when > 0 (e.g. 1080).
	Height int
}

// Render concatenates the plan into outPath. Progress receives seconds
// written so far out of the plan's total duration.
func Render(ctx context.Context, bins ffmpeg.Bins, plan Plan, outPath string, opts RenderOptions, progress func(done, total float64)) error {
	if len(plan.Clips) == 0 {
		return fmt.Errorf("nothing selected to export")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	total := plan.TotalDuration()
	cb := func(sec float64) {
		if progress != nil {
			progress(math.Min(sec, total), total)
		}
	}
	switch opts.Mode {
	case ModeEncode:
		return renderEncode(ctx, bins, plan, outPath, opts, cb)
	default:
		return renderCopy(ctx, bins, plan, outPath, cb)
	}
}

// renderCopy uses the concat demuxer with inpoint/outpoint and -c copy.
func renderCopy(ctx context.Context, bins ffmpeg.Bins, plan Plan, outPath string, progress func(float64)) error {
	list, err := os.CreateTemp(filepath.Dir(outPath), ".longcut-concat-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(list.Name())
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for _, c := range plan.Clips {
		fmt.Fprintf(&b, "file '%s'\ninpoint %.3f\noutpoint %.3f\n", concatEscape(c.Path), c.Start, c.End)
	}
	if _, err := list.WriteString(b.String()); err != nil {
		list.Close()
		return err
	}
	list.Close()
	args := []string{
		"-f", "concat", "-safe", "0", "-i", list.Name(),
		"-c", "copy", "-map", "0:v:0", "-map", "0:a?",
		"-avoid_negative_ts", "make_zero", "-fflags", "+genpts",
		"-movflags", "+faststart",
		outPath,
	}
	return bins.Run(ctx, args, progress)
}

func concatEscape(p string) string {
	p = filepath.ToSlash(p)
	return strings.ReplaceAll(p, "'", `'\''`)
}

// renderEncode builds a trim/concat filter graph and re-encodes.
func renderEncode(ctx context.Context, bins ffmpeg.Bins, plan Plan, outPath string, opts RenderOptions, progress func(float64)) error {
	if opts.CRF <= 0 {
		opts.CRF = 18
	}
	if opts.Preset == "" {
		opts.Preset = "fast"
	}
	var args []string
	var fc strings.Builder
	for i, c := range plan.Clips {
		args = append(args, "-ss", fmt.Sprintf("%.3f", c.Start), "-to", fmt.Sprintf("%.3f", c.End), "-i", c.Path)
		scale := ""
		if opts.Height > 0 {
			scale = fmt.Sprintf(",scale=-2:%d", opts.Height)
		}
		fmt.Fprintf(&fc, "[%d:v:0]setpts=PTS-STARTPTS%s,format=yuv420p[v%d];", i, scale, i)
		fmt.Fprintf(&fc, "[%d:a:0]asetpts=PTS-STARTPTS,aresample=48000[a%d];", i, i)
	}
	for i := range plan.Clips {
		fmt.Fprintf(&fc, "[v%d][a%d]", i, i)
	}
	fmt.Fprintf(&fc, "concat=n=%d:v=1:a=1[v][a]", len(plan.Clips))
	args = append(args,
		"-filter_complex", fc.String(),
		"-map", "[v]", "-map", "[a]",
		"-c:v", "libx264", "-crf", fmt.Sprint(opts.CRF), "-preset", opts.Preset,
		"-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
		outPath,
	)
	return bins.Run(ctx, args, progress)
}

// ---- EDL (CMX 3600) ----

// timecode formats seconds as HH:MM:SS:FF at the given frame rate.
func timecode(sec, fps float64) string {
	if fps <= 0 {
		fps = 30
	}
	ifps := int(math.Round(fps))
	totalFrames := int64(math.Round(sec * fps))
	f := totalFrames % int64(ifps)
	s := totalFrames / int64(ifps)
	return fmt.Sprintf("%02d:%02d:%02d:%02d", s/3600, (s%3600)/60, s%60, f)
}

// EDL renders a CMX 3600 edit list. Each clip becomes one AA/V event whose
// source is named via FROM CLIP NAME, which Resolve and Premiere use to
// relink media.
func EDL(plan Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TITLE: %s\nFCM: NON-DROP FRAME\n\n", sanitizeTitle(plan.Title))
	var rec float64
	for i, c := range plan.Clips {
		fps := c.FPS
		recIn, recOut := rec, rec+c.Len()
		fmt.Fprintf(&b, "%03d  AX       AA/V  C        %s %s %s %s\n", i+1,
			timecode(c.Start, fps), timecode(c.End, fps), timecode(recIn, fps), timecode(recOut, fps))
		fmt.Fprintf(&b, "* FROM CLIP NAME: %s\n", filepath.Base(c.Path))
		notes := c.Notes
		if len(notes) == 0 && c.Label != "" {
			notes = []string{c.Label}
		}
		for _, n := range notes {
			fmt.Fprintf(&b, "* COMMENT: %s\n", oneLine(n))
		}
		b.WriteString("\n")
		rec = recOut
	}
	return b.String()
}

// oneLine flattens a note onto a single EDL line (comments are line-based).
func oneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func sanitizeTitle(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "Longcut"
	}
	return strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, t)
}

// ---- Final Cut Pro 7 XML (xmeml) ----

// XMEML renders an FCP7 XML sequence. Premiere Pro (File > Import) and
// DaVinci Resolve (Import > Timeline) both read this format.
func XMEML(plan Plan) string {
	if len(plan.Clips) == 0 {
		return ""
	}
	fps := plan.Clips[0].FPS
	timebase, ntsc := rateOf(fps)
	w, h := plan.Clips[0].Width, plan.Clips[0].Height
	frames := func(sec float64) int64 { return int64(math.Round(sec * fps)) }

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<!DOCTYPE xmeml>\n<xmeml version=\"5\">\n")
	fmt.Fprintf(&b, "  <sequence id=\"sequence-1\">\n    <name>%s</name>\n    <duration>%d</duration>\n", xmlEsc(sanitizeTitle(plan.Title)), frames(plan.TotalDuration()))
	fmt.Fprintf(&b, "    <rate><timebase>%d</timebase><ntsc>%s</ntsc></rate>\n", timebase, ntsc)
	b.WriteString("    <media>\n      <video>\n        <format><samplecharacteristics>\n")
	fmt.Fprintf(&b, "          <rate><timebase>%d</timebase><ntsc>%s</ntsc></rate>\n          <width>%d</width><height>%d</height>\n", timebase, ntsc, w, h)
	b.WriteString("          <pixelaspectratio>square</pixelaspectratio><fielddominance>none</fielddominance>\n        </samplecharacteristics></format>\n        <track>\n")

	fileIDs := map[string]string{}
	fileDefined := map[string]bool{}
	fileID := func(p string) string {
		if id, ok := fileIDs[p]; ok {
			return id
		}
		id := fmt.Sprintf("file-%d", len(fileIDs)+1)
		fileIDs[p] = id
		return id
	}
	writeFile := func(c Clip) {
		id := fileID(c.Path)
		if fileDefined[id] {
			fmt.Fprintf(&b, "            <file id=\"%s\"/>\n", id)
			return
		}
		fileDefined[id] = true
		fmt.Fprintf(&b, "            <file id=\"%s\">\n              <name>%s</name>\n              <pathurl>%s</pathurl>\n", id, xmlEsc(filepath.Base(c.Path)), xmlEsc(pathURL(c.Path)))
		fmt.Fprintf(&b, "              <rate><timebase>%d</timebase><ntsc>%s</ntsc></rate>\n              <duration>%d</duration>\n", timebase, ntsc, frames(c.Duration))
		fmt.Fprintf(&b, "              <media>\n                <video><samplecharacteristics><rate><timebase>%d</timebase><ntsc>%s</ntsc></rate><width>%d</width><height>%d</height></samplecharacteristics></video>\n", timebase, ntsc, c.Width, c.Height)
		b.WriteString("                <audio><samplecharacteristics><depth>16</depth><samplerate>48000</samplerate></samplecharacteristics><channelcount>2</channelcount></audio>\n              </media>\n            </file>\n")
	}

	var rec float64
	for i, c := range plan.Clips {
		start, end := frames(rec), frames(rec+c.Len())
		in, out := frames(c.Start), frames(c.End)
		fmt.Fprintf(&b, "          <clipitem id=\"clipitem-v%d\">\n            <name>%s</name>\n            <duration>%d</duration>\n", i+1, xmlEsc(filepath.Base(c.Path)), frames(c.Duration))
		fmt.Fprintf(&b, "            <rate><timebase>%d</timebase><ntsc>%s</ntsc></rate>\n            <start>%d</start><end>%d</end><in>%d</in><out>%d</out>\n", timebase, ntsc, start, end, in, out)
		writeFile(c)
		if notes := c.Notes; len(notes) > 0 {
			// Premiere shows mastercomment1 as the clip "Comment" column;
			// Resolve keeps it as clip notes.
			fmt.Fprintf(&b, "            <comments><mastercomment1>%s</mastercomment1></comments>\n", xmlEsc(strings.Join(notes, " | ")))
		}
		fmt.Fprintf(&b, "            <link><linkclipref>clipitem-v%d</linkclipref><mediatype>video</mediatype><trackindex>1</trackindex><clipindex>%d</clipindex></link>\n", i+1, i+1)
		fmt.Fprintf(&b, "            <link><linkclipref>clipitem-a%d</linkclipref><mediatype>audio</mediatype><trackindex>1</trackindex><clipindex>%d</clipindex></link>\n", i+1, i+1)
		b.WriteString("          </clipitem>\n")
		rec += c.Len()
	}
	b.WriteString("        </track>\n      </video>\n      <audio>\n        <format><samplecharacteristics><depth>16</depth><samplerate>48000</samplerate></samplecharacteristics></format>\n        <track>\n")
	rec = 0
	for i, c := range plan.Clips {
		start, end := frames(rec), frames(rec+c.Len())
		in, out := frames(c.Start), frames(c.End)
		fmt.Fprintf(&b, "          <clipitem id=\"clipitem-a%d\">\n            <name>%s</name>\n            <duration>%d</duration>\n", i+1, xmlEsc(filepath.Base(c.Path)), frames(c.Duration))
		fmt.Fprintf(&b, "            <rate><timebase>%d</timebase><ntsc>%s</ntsc></rate>\n            <start>%d</start><end>%d</end><in>%d</in><out>%d</out>\n", timebase, ntsc, start, end, in, out)
		fmt.Fprintf(&b, "            <file id=\"%s\"/>\n            <sourcetrack><mediatype>audio</mediatype><trackindex>1</trackindex></sourcetrack>\n", fileID(c.Path))
		fmt.Fprintf(&b, "            <link><linkclipref>clipitem-v%d</linkclipref><mediatype>video</mediatype><trackindex>1</trackindex><clipindex>%d</clipindex></link>\n", i+1, i+1)
		fmt.Fprintf(&b, "            <link><linkclipref>clipitem-a%d</linkclipref><mediatype>audio</mediatype><trackindex>1</trackindex><clipindex>%d</clipindex></link>\n", i+1, i+1)
		b.WriteString("          </clipitem>\n")
		rec += c.Len()
	}
	b.WriteString("        </track>\n      </audio>\n    </media>\n  </sequence>\n</xmeml>\n")
	return b.String()
}

// rateOf maps a frame rate to an xmeml timebase + NTSC flag.
func rateOf(fps float64) (int, string) {
	tb := int(math.Round(fps))
	if tb <= 0 {
		return 30, "FALSE"
	}
	ntsc := "FALSE"
	// 29.97, 59.94, 23.976 are NTSC rates
	if math.Abs(fps-float64(tb)) > 0.01 {
		ntsc = "TRUE"
	}
	return tb, ntsc
}

func pathURL(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs // Windows drive letter
	}
	u := url.URL{Scheme: "file", Host: "localhost", Path: abs}
	return u.String()
}

func xmlEsc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
