import { useState } from 'react'
import { api, errText } from '../api'
import type { ProjectView, VideoView } from '../types'
import { dur } from '../format'

interface Props {
  project: ProjectView
  running: boolean
  onChange: (pv: ProjectView) => void
  onClose: () => void
  notify: (t: string, k?: 'info' | 'error') => void
}

function fmtDate(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  return d.toLocaleString('ru-RU', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/** Gap between the end of one recording and the start of the next. */
function gap(prev: VideoView, cur: VideoView): string {
  if (!prev.recorded_at || !cur.recorded_at) return ''
  const end = new Date(prev.recorded_at).getTime() + prev.duration * 1000
  const start = new Date(cur.recorded_at).getTime()
  const g = (start - end) / 1000
  if (isNaN(g)) return ''
  if (g < -5) return `перекрытие ${dur(-g)}`
  if (g < 5) return 'встык'
  return `пауза ${dur(g)}`
}

export default function FilesDialog({ project, running, onChange, onClose, notify }: Props) {
  const [busy, setBusy] = useState(false)
  const videos = project.videos
  const outOfOrder = videos.some((v, i) => i > 0 && v.recorded_at && videos[i - 1].recorded_at && v.recorded_at < videos[i - 1].recorded_at)

  const apply = async (fn: () => Promise<ProjectView>) => {
    setBusy(true)
    try { onChange(await fn()) } catch (e) { notify(errText(e), 'error') } finally { setBusy(false) }
  }
  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir
    if (j < 0 || j >= videos.length) return
    const ids = videos.map((v) => v.id)
    ;[ids[i], ids[j]] = [ids[j], ids[i]]
    void apply(() => api.setVideoOrder(ids))
  }
  const remove = (v: VideoView) => {
    if (!confirm(`Убрать «${v.name}» из проекта? Его сегменты, описания и оценки будут удалены (ответы API останутся в кэше).`)) return
    void apply(() => api.removeVideo(v.id))
  }

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onClose}>
      <div className="modal files" onClick={(e) => e.stopPropagation()} role="dialog" aria-label="Файлы проекта">
        <h2>Файлы проекта</h2>
        <p className="dim small">
          Порядок файлов задаёт таймлайн и контекст для оценки. Дата начала записи берётся из имени файла
          (ShadowPlay, OBS, Game Bar), иначе из метаданных, иначе из времени файла минус длительность.
          {outOfOrder && <span className="error"> Сейчас файлы идут не по дате.</span>}
        </p>
        <div className="files-table">
          <div className="files-head"><span>#</span><span>Файл</span><span>Начало записи</span><span>Длительность</span><span>Сегм.</span><span>Стык</span><span></span></div>
          {videos.map((v, i) => (
            <div key={v.id} className="files-row">
              <span className="mono dim">{i + 1}</span>
              <span className="name" title={v.path}>{v.name}</span>
              <span className="mono">{fmtDate(v.recorded_at)}</span>
              <span className="mono">{dur(v.duration)}</span>
              <span className="mono">{v.segments}</span>
              <span className="dim small">{i > 0 ? gap(videos[i - 1], v) : ''}</span>
              <span className="row-actions">
                <button className="icon" onClick={() => move(i, -1)} disabled={busy || running || i === 0} title="Выше">↑</button>
                <button className="icon" onClick={() => move(i, 1)} disabled={busy || running || i === videos.length - 1} title="Ниже">↓</button>
                <button className="icon danger" onClick={() => remove(v)} disabled={busy || running} title="Убрать из проекта">×</button>
              </span>
            </div>
          ))}
          {videos.length === 0 && <div className="files-row dim">Файлов пока нет.</div>}
        </div>
        <div className="modal-actions">
          <button onClick={() => apply(api.sortVideosByDate)} disabled={busy || running || videos.length < 2}>Расставить по дате</button>
          <div className="spacer" />
          <button onClick={onClose} disabled={busy}>Закрыть</button>
        </div>
      </div>
    </div>
  )
}
