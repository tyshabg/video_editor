# Longcut

A desktop tool for editing no-commentary game longplays. It splits raw recordings
into segments by scene and motion, describes each segment with Claude, rates its
narrative importance (1–10) with context from neighbouring segments, and renders the
final cut (mp4 + EDL / FCP7 XML for Premiere and DaVinci Resolve).

Stack: Go + Wails v2 (React/TS in a native window), ffmpeg, SQLite (modernc, no CGO),
Anthropic SDK. A single binary, no local servers.

## Requirements

- Go 1.26+, Node 20+, Wails CLI v2 (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
- Windows: WebView2 (ships with Windows 10/11). macOS: Xcode Command Line Tools.
- ffmpeg + ffprobe: from `LONGCUT_FFMPEG`, from `<data>/bin`, from PATH, or via the
  "Download" button in Settings.
- `ANTHROPIC_API_KEY` in the environment (or enter it in Settings for the session).

## Build

```sh
wails build                             # build/bin/Longcut.exe | Longcut.app
go build -o bin/longcut ./cmd/longcut   # CLI
go test ./...                           # tests: scene detection, JSON parser, store, export
```

## Data directory

The project database, frame and preview caches, downloaded ffmpeg and the default
export location all live in the data directory. It can grow to gigabytes, so it can be
relocated:

- in the app: Settings → Data folder → "Change…" (moves existing data);
- from the CLI: `longcut datadir D:\LongcutData` (no move).

The choice is stored in `config.json` next to the default location
(`%LOCALAPPDATA%\Longcut` on Windows, `~/Library/Application Support/Longcut` on macOS,
`$XDG_DATA_HOME/Longcut` on Linux). Resolution order: `LONGCUT_DATA_DIR` environment
variable → `config.json` → platform default.

## CLI

```sh
longcut run -p kenshi-s1 -game Kenshi D:\Videos\kenshi     # everything: add → analyze → frames → describe → score
longcut status -p kenshi-s1                                 # progress, spend so far, remaining cost estimate
longcut segments -p kenshi-s1 -min-score 6                  # list segments with descriptions and scores
longcut export -p kenshi-s1 -threshold 6 -out cut.mp4 -edl -xml
longcut export -p kenshi-s1 -out cut.edl -no-video -xml     # edit list only, no render
longcut describe -p kenshi-s1 -mock                         # dry run without the API, cost estimate only
longcut projects                                            # list projects
longcut doctor                                              # check ffmpeg, data dir and API key
```

Common flags accepted by every command:

| Flag | Meaning |
| --- | --- |
| `-p NAME` | project name |
| `-game NAME` | game title used in prompts (stored on the project) |
| `-data DIR` | data directory (default: `LONGCUT_DATA_DIR` or the OS app dir) |
| `-model ID` | Claude model (default `claude-sonnet-5`) |
| `-lang CODE` | description language: `ru`, `en` (default `ru`) |
| `-concurrency N` | parallel ffmpeg/API workers (default 4) |
| `-min` / `-max SEC` | segment length bounds (default 5 / 60) |
| `-cut F` | hard-cut threshold 0..1 (default 0.30) |
| `-sample MODE` | `keyframes` or `fps` (default `keyframes`) |
| `-fps F` | sample rate for `-sample fps` (default 2) |
| `-no-hwaccel` | disable GPU decoding |
| `-force` | re-segment even if parameters are unchanged |
| `-mock` | offline mode: no API calls, estimated costs only |
| `-limit N` | process at most N segments this run |
| `-v` | verbose logs |

Any stage can be interrupted (Ctrl+C) and started again: progress is kept in SQLite
per segment status, and every API response is cached by a hash of its inputs (model,
prompt version, frames / context), so a re-run does not pay twice.

## How it works

1. **Analysis** — ffmpeg emits only keyframes (GPU decode, `-skip_frame nokey`),
   downscaled to 160×90 grayscale. Go computes inter-frame motion and histogram
   distance; hard cuts, gradual transitions and activity changes become boundaries.
   Segments are 5–60 s. Full 2 fps decoding kicks in automatically when keyframes are
   more than 2.5 s apart.
2. **Frames** — 3–5 frames per segment, 1280×720 JPEG q70 (~1.2k tokens each).
3. **Description** — `claude-sonnet-5` by default, 1–2 sentences per segment.
4. **Scoring** — the same model returns JSON `{"score", "reason"}` via structured
   outputs, with the 8 previous segments as context. Manual scores and explicit
   include/exclude flags take priority.
5. **Export** — stream copy through the concat demuxer (cuts on keyframes, instant) or
   libx264 re-encoding for frame accuracy; CMX 3600 EDL and xmeml.

Recordings are ordered chronologically by the start time parsed from the file name
(ShadowPlay, OBS, Xbox Game Bar, Medal and `YYYYMMDD_HHMMSS` patterns), regardless of
the order they were added.

Cost guide for claude-sonnet-5: about $0.025 per segment, i.e. roughly $4–5 for a
three-hour longplay. The estimate is shown before a run and after it.
