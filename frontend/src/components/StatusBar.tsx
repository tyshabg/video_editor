import type { ProjectView } from '../types'
import type { RunState } from '../App'
import { dur, heat, usd } from '../format'

interface Props {
  project: ProjectView | null
  threshold: number
  onThreshold: (v: number) => void
  kept: { count: number; duration: number }
  run: RunState
  onExport: () => void
}

export default function StatusBar({ project, threshold, onThreshold, kept, run, onExport }: Props) {
  if (!project) return <footer className="statusbar" />
  const est = project.estimate
  const spent = project.usage?.['']?.cost_usd ?? 0
  const [bg] = heat(threshold)
  return (
    <footer className="statusbar">
      <div className="threshold">
        <span className="label">Порог</span>
        <input type="range" min={1} max={10} step={1} value={threshold} onChange={(e) => onThreshold(Number(e.target.value))}
          style={{ '--h': bg } as React.CSSProperties} aria-label="Порог значимости" />
        <span className="thr-value" style={{ background: bg }}>{threshold}</span>
        <span className="dim">сегменты ниже порога затемнены и не идут в экспорт</span>
      </div>
      <div className="spacer" />
      <div className="stat">
        <span className="label">В монтаже</span>
        <span className="mono">{kept.count}/{project.segments.length}</span>
        <span className="dim">·</span>
        <span className="mono">{dur(kept.duration)}</span>
        <span className="dim">из {dur(project.duration)}</span>
      </div>
      <div className="stat" title={`${est.spent_calls} запросов, ${est.spent_input_tokens.toLocaleString('ru')} входных токенов`}>
        <span className="label">API</span>
        <span className="mono">потрачено {usd(spent)}</span>
        {est.remaining_usd > 0 && <span className="dim">· осталось ≈ {usd(est.remaining_usd)} ({est.model})</span>}
      </div>
      <button className="primary" onClick={onExport} disabled={run.running || kept.count === 0}>Экспорт…</button>
    </footer>
  )
}
