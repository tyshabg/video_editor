// Thin typed wrapper around the Wails-generated bindings.
import * as Go from '../wailsjs/go/main/App'
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime'
import type {
  ProjectSummary, ProjectView, SettingsView, ModelInfo, PlanView, ExportOptions, ProgressEvent, RunResult,
} from './types'

const g = Go as unknown as Record<string, (...a: any[]) => Promise<any>>

// Go nil slices arrive as null: normalise so components can rely on arrays.
function normalize(pv: ProjectView): ProjectView {
  return {
    ...pv,
    videos: pv.videos ?? [],
    segments: (pv.segments ?? []).map((s) => ({ ...s, thumbs: s.thumbs ?? [] })),
    usage: pv.usage ?? {},
  }
}

export const api = {
  listProjects: (): Promise<ProjectSummary[]> => g.ListProjects().then((r) => r ?? []),
  createProject: (name: string, game: string): Promise<ProjectView> => g.CreateProject(name, game).then(normalize),
  openProject: (name: string): Promise<ProjectView> => g.OpenProject(name).then(normalize),
  deleteProject: (name: string): Promise<void> => g.DeleteProject(name),
  setGame: (game: string): Promise<ProjectView> => g.SetGame(game).then(normalize),
  refresh: (): Promise<ProjectView> => g.Refresh().then(normalize),

  pickVideoFiles: (): Promise<string[]> => g.PickVideoFiles().then((r) => r ?? []),
  pickVideoFolder: (): Promise<string> => g.PickVideoFolder().then((r) => r ?? ''),
  addPaths: (paths: string[]): Promise<ProjectView> => g.AddPaths(paths).then(normalize),
  sortVideosByDate: (): Promise<ProjectView> => g.SortVideosByDate().then(normalize),
  setVideoOrder: (ids: number[]): Promise<ProjectView> => g.SetVideoOrder(ids).then(normalize),
  removeVideo: (id: number): Promise<ProjectView> => g.RemoveVideo(id).then(normalize),

  run: (stages: string[]): Promise<void> => g.Run(stages),
  cancel: (): Promise<void> => g.Cancel(),

  setManualScore: (id: number, score: number): Promise<void> => g.SetManualScore(id, score),
  setSelected: (id: number, sel: number): Promise<void> => g.SetSelected(id, sel),
  resetScores: (): Promise<void> => g.ResetScores(),

  proxyURL: (id: number): Promise<string> => g.ProxyURL(id),

  exportPlan: (threshold: number): Promise<PlanView> => g.ExportPlan(threshold),
  pickExportPath: (name: string, kind: 'mp4' | 'edl'): Promise<string> => g.PickExportPath(name, kind).then((r) => r ?? ''),
  export: (o: ExportOptions): Promise<void> => g.Export(o),
  showInFolder: (p: string): Promise<void> => g.ShowInFolder(p),

  getSettings: (): Promise<SettingsView> => g.GetSettings(),
  setAPIKey: (k: string, remember: boolean): Promise<void> => g.SetAPIKey(k, remember),
  setModel: (m: string): Promise<void> => g.SetModel(m),
  setLang: (l: string): Promise<void> => g.SetLang(l),
  models: (): Promise<ModelInfo[]> => g.Models().then((r) => r ?? []),
  recheckFFmpeg: (): Promise<SettingsView> => g.RecheckFFmpeg(),
  dataDirSize: (): Promise<number> => g.DataDirSize(),
  pickDataDir: (): Promise<string> => g.PickDataDir().then((r) => r ?? ''),
  setDataDir: (path: string, move: boolean): Promise<void> => g.SetDataDir(path, move),
  openDataDir: (): Promise<void> => g.OpenDataDir(),
  downloadFFmpeg: (): Promise<void> => g.DownloadFFmpeg(),
}

// Several components listen to the same event (e.g. "progress" in the top
// bar, the export dialog and the settings dialog). Wails' EventsOff removes
// every listener of that name, so unsubscribing must go through the cancel
// function EventsOn returns, never EventsOff.
function on<T>(name: string, cb: (v: T) => void): () => void {
  const cancel = EventsOn(name, cb as (...data: any) => void)
  return typeof cancel === 'function' ? cancel : () => {}
}

export const onProgress = (cb: (e: ProgressEvent) => void) => on<ProgressEvent>('progress', cb)
export const onDone = (cb: (r: RunResult) => void) => on<RunResult>('done', cb)
export const onExportDone = (cb: (r: RunResult) => void) => on<RunResult>('export-done', cb)
export const onDataDirDone = (cb: (r: RunResult) => void) => on<RunResult>('data-dir-done', cb)
export const onDataDirChanged = (cb: (path: string) => void) => on<string>('data-dir-changed', cb)
export const onFFmpegDone = (cb: (r: RunResult) => void) => on<RunResult>('ffmpeg-done', cb)

export function errText(e: unknown): string {
  if (typeof e === 'string') return e
  if (e && typeof e === 'object' && 'message' in e) return String((e as Error).message)
  return String(e)
}
