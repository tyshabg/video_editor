export interface ProjectSummary {
  name: string
  game: string
  videos: number
  segments: number
  scored: number
  duration: number
  spent_usd: number
  updated: string
}

export interface VideoView {
  id: number
  name: string
  path: string
  duration: number
  offset: number
  width: number
  height: number
  fps: number
  analyzed: boolean
  segments: number
  recorded_at: string
}

export interface SegmentView {
  id: number
  video_id: number
  idx: number
  start: number
  end: number
  offset: number
  motion_avg: number
  cut_score: number
  status: string
  description: string
  score: number | null
  score_reason: string
  score_manual: number | null
  selected: boolean | null
  thumbs: string[] | null
  error: string
}

export interface Usage {
  calls: number
  input_tokens: number
  output_tokens: number
  cache_read: number
  cost_usd: number
}

export interface Estimate {
  model: string
  segments: number
  to_describe: number
  to_score: number
  describe_usd: number
  score_usd: number
  remaining_usd: number
  spent_usd: number
  spent_calls: number
  spent_input_tokens: number
  unknown_pricing: boolean
}

export interface ProjectView {
  name: string
  game: string
  videos: VideoView[]
  segments: SegmentView[]
  duration: number
  usage: Record<string, Usage>
  estimate: Estimate
}

export interface SegmentDelta {
  id: number
  status: string
  description?: string
  score?: number
  reason?: string
  model?: string
}

export interface ProgressEvent {
  stage: string
  video: string
  done: number
  total: number
  message?: string
  cost_usd: number
  hits: number
  seg?: SegmentDelta
}

export interface RunResult {
  error: string
  cost_usd: number
  hits: number
  elapsed: string
}

export interface SettingsView {
  data_dir: string
  data_dir_source: string
  data_dir_env: boolean
  config_path: string
  ffmpeg_path: string
  ffmpeg_version: string
  ffmpeg_error: string
  api_key_set: boolean
  api_key_hint: string
  api_key_source: string
  model: string
  lang: string
  running: boolean
  version: string
}

export interface ModelInfo {
  id: string
  input: number
  output: number
}

export interface PlanView {
  clips: number
  duration: number
  items: { video: string; start: number; end: number; label: string }[]
}

export interface ExportOptions {
  threshold: number
  out: string
  encode: boolean
  height: number
  edl: boolean
  xml: boolean
  no_video: boolean
}

/** Effective score: manual override wins, then model score, else 0. */
export function effectiveScore(s: SegmentView): number {
  if (s.score_manual != null) return s.score_manual
  if (s.score != null) return s.score
  return 0
}

/** Whether a segment is part of the cut at the given threshold. */
export function isKept(s: SegmentView, threshold: number): boolean {
  if (s.selected != null) return s.selected
  if (s.score == null && s.score_manual == null) return false
  return effectiveScore(s) >= threshold
}
