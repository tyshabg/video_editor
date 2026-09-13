import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, errText, onDataDirChanged, onDone, onProgress } from './api'
import type { ProgressEvent, ProjectView, RunResult, SegmentView } from './types'
import { isKept } from './types'
import TopBar from './components/TopBar'
import Timeline from './components/Timeline'
import Inspector from './components/Inspector'
import StatusBar from './components/StatusBar'
import ProjectPicker from './components/ProjectPicker'
import ExportDialog from './components/ExportDialog'
import SettingsDialog from './components/SettingsDialog'
import FilesDialog from './components/FilesDialog'
import Toast from './components/Toast'

export interface RunState {
  running: boolean
  event: ProgressEvent | null
  last: RunResult | null
}

export default function App() {
  const [project, setProject] = useState<ProjectView | null>(null)
  const [pickerOpen, setPickerOpen] = useState(true)
  const [exportOpen, setExportOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [filesOpen, setFilesOpen] = useState(false)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [threshold, setThresholdState] = useState(5)
  const [run, setRun] = useState<RunState>({ running: false, event: null, last: null })
  const [toast, setToast] = useState<{ text: string; kind: 'info' | 'error' } | null>(null)
  const [lastUpdated, setLastUpdated] = useState<{ id: number; at: number } | null>(null)
  const refreshTimer = useRef<number | null>(null)

  const notify = useCallback((text: string, kind: 'info' | 'error' = 'info') => setToast({ text, kind }), [])

  // Persist threshold per project (per-viewer convenience only).
  useEffect(() => {
    if (!project) return
    try {
      const v = localStorage.getItem(`longcut.threshold.${project.name}`)
      if (v) setThresholdState(Number(v))
    } catch { /* ignore */ }
  }, [project?.name])
  const setThreshold = (v: number) => {
    setThresholdState(v)
    if (project) {
      try { localStorage.setItem(`longcut.threshold.${project.name}`, String(v)) } catch { /* ignore */ }
    }
  }

  const reload = useCallback(async () => {
    try {
      const pv = await api.refresh()
      setProject(pv)
    } catch (e) {
      // no project open yet
    }
  }, [])

  // Pipeline events: throttle full reloads while a stage is streaming results.
  useEffect(() => {
    const offP = onProgress((e) => {
      setRun((r) => ({ ...r, running: true, event: e }))
      if (e.seg) {
        // Apply the result in place the moment it lands.
        const d = e.seg
        setLastUpdated({ id: d.id, at: Date.now() })
        setProject((p) => p ? {
          ...p,
          segments: p.segments.map((s) => s.id !== d.id ? s : {
            ...s,
            status: d.status,
            description: d.description ?? s.description,
            score: d.score ?? s.score,
            score_reason: d.reason ?? s.score_reason,
            error: '',
          }),
        } : p)
        return
      }
      if ((e.stage === 'describe' || e.stage === 'score' || e.stage === 'segment' || e.stage === 'frames') && refreshTimer.current == null) {
        refreshTimer.current = window.setTimeout(() => {
          refreshTimer.current = null
          void reload()
        }, 1500)
      }
    })
    const offD = onDone((res) => {
      setRun({ running: false, event: null, last: res })
      void reload()
      if (res.error) notify(res.error, 'error')
      else notify(`Готово за ${res.elapsed}. Потрачено ${res.cost_usd.toFixed(3)} $, из кэша: ${res.hits}`)
    })
    const offC = onDataDirChanged((path) => {
      setProject(null)
      setSelectedId(null)
      setPickerOpen(true)
      notify(`Папка данных: ${path}`)
    })
    return () => { offP(); offD(); offC() }
  }, [reload, notify])

  const openProject = async (name: string, quiet = false) => {
    try {
      const pv = await api.openProject(name)
      setProject(pv)
      setSelectedId(pv.segments.length > 0 ? pv.segments[0].id : null)
      setPickerOpen(false)
      try { localStorage.setItem('longcut.lastProject', name) } catch { /* ignore */ }
    } catch (e) {
      if (!quiet) notify(errText(e), 'error')
    }
  }

  // Reopen the last project on launch so the app starts in a working state.
  useEffect(() => {
    let last: string | null = null
    try { last = localStorage.getItem('longcut.lastProject') } catch { /* ignore */ }
    if (last) void openProject(last, true)
  }, [])

  const createProject = async (name: string, game: string) => {
    try {
      const pv = await api.createProject(name, game)
      setProject(pv)
      setSelectedId(null)
      setPickerOpen(false)
      try { localStorage.setItem('longcut.lastProject', name) } catch { /* ignore */ }
    } catch (e) {
      notify(errText(e), 'error')
    }
  }

  const addFiles = async (folder: boolean) => {
    try {
      let paths: string[] = []
      if (folder) {
        const d = await api.pickVideoFolder()
        if (d) paths = [d]
      } else {
        paths = await api.pickVideoFiles()
      }
      if (paths.length === 0) return
      notify('Читаю метаданные файлов…')
      const pv = await api.addPaths(paths)
      setProject(pv)
      notify(`Файлов в проекте: ${pv.videos.length}`)
    } catch (e) {
      notify(errText(e), 'error')
    }
  }

  const startRun = async (stages: string[]) => {
    try {
      await api.run(stages)
      setRun({ running: true, event: null, last: null })
    } catch (e) {
      notify(errText(e), 'error')
    }
  }

  const cancelRun = async () => { await api.cancel() }

  const updateSegment = (id: number, patch: Partial<SegmentView>) => {
    setProject((p) => p ? { ...p, segments: p.segments.map((s) => s.id === id ? { ...s, ...patch } : s) } : p)
  }

  const setManualScore = async (id: number, score: number | null) => {
    try {
      await api.setManualScore(id, score ?? 0)
      updateSegment(id, { score_manual: score })
    } catch (e) { notify(errText(e), 'error') }
  }

  const setSelected = async (id: number, sel: boolean | null) => {
    try {
      await api.setSelected(id, sel == null ? -1 : sel ? 1 : 0)
      updateSegment(id, { selected: sel })
    } catch (e) { notify(errText(e), 'error') }
  }

  const segments = project?.segments ?? []
  const selected = useMemo(() => segments.find((s) => s.id === selectedId) ?? null, [segments, selectedId])
  const keptStats = useMemo(() => {
    let n = 0, d = 0
    for (const s of segments) if (isKept(s, threshold)) { n++; d += s.end - s.start }
    return { count: n, duration: d }
  }, [segments, threshold])

  // Keyboard: arrows move selection, 1-9/0 set manual score, Delete clears, I/X include/exclude.
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      const t = ev.target as HTMLElement
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT')) return
      if (!selected) return
      const i = segments.findIndex((s) => s.id === selected.id)
      if (ev.key === 'ArrowRight' && i < segments.length - 1) { setSelectedId(segments[i + 1].id); ev.preventDefault() }
      else if (ev.key === 'ArrowLeft' && i > 0) { setSelectedId(segments[i - 1].id); ev.preventDefault() }
      else if (/^[0-9]$/.test(ev.key)) { void setManualScore(selected.id, ev.key === '0' ? 10 : Number(ev.key)) }
      else if (ev.key === 'Delete' || ev.key === 'Backspace') { void setManualScore(selected.id, null) }
      else if (ev.key === 'i' || ev.key === 'ш') { void setSelected(selected.id, selected.selected === true ? null : true) }
      else if (ev.key === 'x' || ev.key === 'ч') { void setSelected(selected.id, selected.selected === false ? null : false) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [selected, segments])

  return (
    <div className="app">
      <TopBar
        project={project}
        run={run}
        onDismissError={() => setRun((r) => ({ ...r, last: null }))}
        onOpenPicker={() => setPickerOpen(true)}
        onAddFiles={() => addFiles(false)}
        onAddFolder={() => addFiles(true)}
        onFiles={() => setFilesOpen(true)}
        onRun={startRun}
        onCancel={cancelRun}
        onSettings={() => setSettingsOpen(true)}
        onGameChange={async (g) => { try { setProject(await api.setGame(g)) } catch (e) { notify(errText(e), 'error') } }}
      />
      <div className="workspace">
        <div className="main">
          <Timeline
            project={project}
            threshold={threshold}
            selectedId={selectedId}
            onSelect={setSelectedId}
            lastUpdated={lastUpdated}
            running={run.running}
          />
        </div>
        <Inspector
          segment={selected}
          project={project}
          threshold={threshold}
          onManualScore={setManualScore}
          onSelected={setSelected}
          onError={(m) => notify(m, 'error')}
        />
      </div>
      <StatusBar
        project={project}
        threshold={threshold}
        onThreshold={setThreshold}
        kept={keptStats}
        run={run}
        onExport={() => setExportOpen(true)}
      />
      {pickerOpen && (
        <ProjectPicker
          current={project?.name ?? null}
          onOpen={openProject}
          onCreate={createProject}
          onClose={() => project && setPickerOpen(false)}
          onError={(m) => notify(m, 'error')}
        />
      )}
      {exportOpen && project && (
        <ExportDialog project={project} threshold={threshold} onClose={() => setExportOpen(false)} notify={notify} />
      )}
      {settingsOpen && <SettingsDialog onClose={() => setSettingsOpen(false)} notify={notify} />}
      {filesOpen && project && (
        <FilesDialog project={project} running={run.running} onChange={setProject} onClose={() => setFilesOpen(false)} notify={notify} />
      )}
      {toast && <Toast text={toast.text} kind={toast.kind} onHide={() => setToast(null)} />}
    </div>
  )
}
