import { useEffect, useRef, useState } from 'react'
import type { ProjectView } from '../types'
import type { RunState } from '../App'
import { usd } from '../format'

interface Props {
  project: ProjectView | null
  run: RunState
  onDismissError: () => void
  onOpenPicker: () => void
  onAddFiles: () => void
  onAddFolder: () => void
  onFiles: () => void
  onRun: (stages: string[]) => void
  onCancel: () => void
  onSettings: () => void
  onGameChange: (game: string) => void
}

const STAGES: { key: string; label: string; stages: string[]; hint: string }[] = [
  { key: 'analyze', label: 'Анализ', stages: ['analyze'], hint: 'Детекция сцен и нарезка на сегменты (локально, бесплатно)' },
  { key: 'describe', label: 'Описание', stages: ['analyze', 'frames', 'describe'], hint: 'Ключевые кадры → Claude → описание каждого сегмента' },
  { key: 'score', label: 'Оценка', stages: ['score'], hint: 'Оценка значимости 1–10 с контекстом предыдущих сегментов' },
  { key: 'all', label: 'Всё', stages: ['analyze', 'frames', 'describe', 'score'], hint: 'Все этапы подряд' },
]

export default function TopBar({ project, run, onDismissError, onOpenPicker, onAddFiles, onAddFolder, onFiles, onRun, onCancel, onSettings, onGameChange }: Props) {
  const [game, setGame] = useState(project?.game ?? '')
  useEffect(() => setGame(project?.game ?? ''), [project?.game])
  const ev = run.event
  const pct = ev && ev.total > 0 ? Math.min(100, Math.round((ev.done / ev.total) * 100)) : 0
  // ETA from the rate observed since this stage started.
  const rate = useRef<{ stage: string; t0: number; d0: number; eta: string }>({ stage: '', t0: 0, d0: 0, eta: '' })
  if (ev) {
    const r = rate.current
    if (r.stage !== ev.stage) { rate.current = { stage: ev.stage, t0: Date.now(), d0: ev.done, eta: '' } }
    else if (ev.done > r.d0 && ev.total > 0) {
      const perUnit = (Date.now() - r.t0) / (ev.done - r.d0)
      const left = Math.round(((ev.total - ev.done) * perUnit) / 1000)
      if (Date.now() - r.t0 > 5000) r.eta = left >= 3600 ? `≈ ${Math.floor(left / 3600)} ч ${Math.round((left % 3600) / 60)} мин` : left >= 60 ? `≈ ${Math.round(left / 60)} мин` : `≈ ${left} с`
    }
  } else if (!run.running) {
    rate.current = { stage: '', t0: 0, d0: 0, eta: '' }
  }
  const stageName: Record<string, string> = {
    probe: 'Чтение файлов', analyze: 'Анализ сцен', segment: 'Нарезка', frames: 'Кадры', describe: 'Описание', score: 'Оценка', render: 'Рендер', ffmpeg: 'Загрузка ffmpeg',
  }

  return (
    <header className="topbar">
      <button className="brand" onClick={onOpenPicker} title="Проекты">
        <span className="brand-mark">▮▮</span>
        <span className="brand-name">{project ? project.name : 'Longcut'}</span>
        <span className="brand-caret">▾</span>
      </button>
      {project && (
        <label className="game">
          <span>Игра</span>
          <input
            value={game}
            placeholder="Название игры для промптов"
            onChange={(e) => setGame(e.target.value)}
            onBlur={() => game !== (project.game ?? '') && onGameChange(game)}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
          />
        </label>
      )}
      <div className="spacer" />
      {project && !run.running && (
        <>
          <div className="btn-group">
            <button onClick={onAddFiles} title="Добавить видеофайлы">+ Файлы</button>
            <button onClick={onAddFolder} title="Добавить папку с записями">+ Папка</button>
            <button onClick={onFiles} title="Порядок файлов, даты записи, удаление из проекта" disabled={project.videos.length === 0}>Порядок…</button>
          </div>
          <div className="btn-group">
            {STAGES.map((s) => (
              <button key={s.key} className={s.key === 'all' ? 'primary' : ''} title={s.hint} onClick={() => onRun(s.stages)} disabled={project.videos.length === 0}>
                {s.label}
              </button>
            ))}
          </div>
        </>
      )}
      {run.running && (
        <div className="progress" role="status">
          <div className="progress-label">
            <span>{ev ? (stageName[ev.stage] ?? ev.stage) : 'Запуск…'}</span>
            {ev?.video && <span className="dim"> · {ev.video}</span>}
            {ev && ev.total > 0 && <span className="mono"> {ev.done}/{ev.total} ({pct}%)</span>}
            {rate.current.eta && <span className="dim"> · осталось {rate.current.eta}</span>}
            {ev && ev.cost_usd > 0 && <span className="cost"> {usd(ev.cost_usd)}</span>}
          </div>
          <div className="progress-track"><div className="progress-fill" style={{ width: `${pct}%` }} /></div>
          {ev?.message && <div className="progress-msg" title={ev.message}>{ev.message}</div>}
          <button className="danger" onClick={onCancel}>Стоп</button>
        </div>
      )}
      <button className="icon" onClick={onSettings} title="Настройки">⚙</button>
      {!run.running && run.last?.error && (
        <div className="run-error" role="alert">
          <span>Последний запуск остановлен: {run.last.error}</span>
          <button className="icon" onClick={onDismissError} title="Скрыть">×</button>
        </div>
      )}
    </header>
  )
}
