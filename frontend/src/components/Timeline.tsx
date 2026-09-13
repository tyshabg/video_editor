import { useEffect, useMemo, useRef, useState } from 'react'
import type { ProjectView, SegmentView } from '../types'
import { effectiveScore, isKept } from '../types'
import { heat, tc } from '../format'

interface Props {
  project: ProjectView | null
  threshold: number
  selectedId: number | null
  onSelect: (id: number) => void
  lastUpdated: { id: number; at: number } | null
  running: boolean
}

const LANE_H = 104

export default function Timeline({ project, threshold, selectedId, onSelect, lastUpdated, running }: Props) {
  const scroller = useRef<HTMLDivElement>(null)
  const [pps, setPps] = useState(2) // pixels per second
  const [width, setWidth] = useState(1000)
  const [follow, setFollow] = useState(true)
  const duration = project?.duration ?? 0

  // While a stage is running, keep the segment that just changed in view.
  useEffect(() => {
    const el = scroller.current
    if (!el || !follow || !running || !lastUpdated || !project) return
    const s = project.segments.find((x) => x.id === lastUpdated.id)
    if (!s) return
    const x0 = s.offset * pps
    const x1 = (s.offset + s.end - s.start) * pps
    if (x0 < el.scrollLeft + 40 || x1 > el.scrollLeft + el.clientWidth - 40) {
      el.scrollTo({ left: Math.max(0, x0 - el.clientWidth * 0.3), behavior: 'smooth' })
    }
  }, [lastUpdated?.at, follow, running])

  useEffect(() => {
    const el = scroller.current
    if (!el) return
    const ro = new ResizeObserver(() => setWidth(el.clientWidth))
    ro.observe(el)
    setWidth(el.clientWidth)
    return () => ro.disconnect()
  }, [])

  // Fit to width when a project opens.
  useEffect(() => {
    if (duration > 0 && width > 0) setPps(Math.max(0.02, (width - 24) / duration))
  }, [project?.name, duration > 0])

  const fit = () => duration > 0 && setPps(Math.max(0.02, (width - 24) / duration))
  const zoom = (f: number, anchorX?: number) => {
    const el = scroller.current
    const next = Math.min(80, Math.max(0.02, pps * f))
    if (el && anchorX != null) {
      const t = (el.scrollLeft + anchorX) / pps
      setPps(next)
      requestAnimationFrame(() => { el.scrollLeft = t * next - anchorX })
    } else setPps(next)
  }

  // Ctrl+wheel zooms around the cursor; plain wheel scrolls horizontally.
  useEffect(() => {
    const el = scroller.current
    if (!el) return
    const h = (e: WheelEvent) => {
      if (e.ctrlKey) {
        e.preventDefault()
        zoom(e.deltaY < 0 ? 1.25 : 0.8, e.clientX - el.getBoundingClientRect().left)
      } else if (Math.abs(e.deltaY) > Math.abs(e.deltaX)) {
        el.scrollLeft += e.deltaY
        e.preventDefault()
      }
    }
    el.addEventListener('wheel', h, { passive: false })
    return () => el.removeEventListener('wheel', h)
  }, [pps])

  // Keep the selected segment in view.
  useEffect(() => {
    const el = scroller.current
    const s = project?.segments.find((x) => x.id === selectedId)
    if (!el || !s) return
    const x0 = s.offset * pps, x1 = s.end === s.start ? x0 : (s.offset + s.end - s.start) * pps
    if (x0 < el.scrollLeft || x1 > el.scrollLeft + el.clientWidth) el.scrollLeft = Math.max(0, x0 - el.clientWidth / 3)
  }, [selectedId])

  const ticks = useMemo(() => {
    const target = 110 // px between labels
    const steps = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200]
    const step = steps.find((s) => s * pps >= target) ?? 7200
    const out: number[] = []
    for (let t = 0; t <= duration; t += step) out.push(t)
    return { step, out }
  }, [pps, duration])

  if (!project) {
    return <div className="timeline empty"><p>Откройте или создайте проект, затем добавьте записи.</p></div>
  }
  const total = Math.max(duration * pps, 10)
  const counts = { total: project.segments.length, kept: 0, described: 0, scored: 0 }
  for (const s of project.segments) {
    if (isKept(s, threshold)) counts.kept++
    if (s.status === 'described' || s.status === 'scored') counts.described++
    if (s.status === 'scored') counts.scored++
  }

  return (
    <div className="timeline">
      <div className="tl-tools">
        <span className="dim">
          {project.videos.length} файл(ов) · {tc(duration)} · сегментов {counts.total}
          {counts.total > 0 && <> · описано <b className="mono">{counts.described}</b> · оценено <b className="mono">{counts.scored}</b></>}
          {' '}· в монтаже <b className="mono">{counts.kept}</b>
        </span>
        <span className="legend-mini" title="Полоска внизу блока: статус обработки">
          <i className="st-detected" /> нарезан <i className="st-described" /> описан <i className="st-scored" /> оценён
        </span>
        <div className="spacer" />
        {running && (
          <label className="check small" title="Прокручивать таймлайн к сегменту, который обрабатывается сейчас">
            <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> следить
          </label>
        )}
        <button className="icon" onClick={() => zoom(0.8)} title="Отдалить">−</button>
        <button className="icon" onClick={() => zoom(1.25)} title="Приблизить">+</button>
        <button onClick={fit} title="Вписать весь проект">Вписать</button>
        <span className="dim mono">{pps >= 1 ? `${pps.toFixed(1)} px/с` : `${(1 / pps).toFixed(0)} с/px`}</span>
      </div>
      <div className="tl-scroll" ref={scroller}>
        <div className="tl-canvas" style={{ width: total + 24 }}>
          <div className="tl-ruler">
            {ticks.out.map((t) => (
              <div key={t} className="tick" style={{ left: t * pps }}>
                <span>{tc(t)}</span>
              </div>
            ))}
          </div>
          <div className="tl-files">
            {project.videos.map((v) => (
              <div key={v.id} className={`file ${v.analyzed ? '' : 'pending'}`} style={{ left: v.offset * pps, width: Math.max(2, v.duration * pps) }} title={v.path}>
                <span>{v.name}</span>
                {!v.analyzed && <span className="dim"> · не проанализирован</span>}
              </div>
            ))}
          </div>
          <div className="tl-lane" style={{ height: LANE_H }}>
            {project.segments.map((s) => (
              <Block key={s.id} s={s} pps={pps} threshold={threshold} selected={s.id === selectedId} onSelect={onSelect}
                pulse={lastUpdated?.id === s.id ? lastUpdated.at : 0} />
            ))}
            {project.segments.length === 0 && project.videos.length > 0 && (
              <div className="lane-hint">Нажмите «Анализ», чтобы нарезать записи на сегменты.</div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function Block({ s, pps, threshold, selected, onSelect, pulse }: { s: SegmentView; pps: number; threshold: number; selected: boolean; onSelect: (id: number) => void; pulse: number }) {
  const w = Math.max(3, (s.end - s.start) * pps - 1)
  const score = effectiveScore(s)
  const scored = s.score != null || s.score_manual != null
  const kept = isKept(s, threshold)
  const [bg, fg] = scored ? heat(score) : ['transparent', 'var(--ink-3)']
  const cls = ['seg', scored ? '' : 'unscored', kept ? '' : 'dropped', selected ? 'selected' : '', s.error ? 'err' : '', 'st-' + (s.status === 'framed' ? 'detected' : s.status)].join(' ')
  const title = `#${s.idx} ${tc(s.start)}–${tc(s.end)}${scored ? ` · оценка ${score}` : ` · ${s.status}`}${s.description ? '\n' + s.description : ''}`
  return (
    <div
      className={cls}
      style={{ left: s.offset * pps, width: w, background: bg, color: fg }}
      title={title}
      onClick={() => onSelect(s.id)}
      role="button"
      tabIndex={-1}
    >
      {w > 22 && scored && <span className="seg-score">{score}</span>}
      {w > 90 && s.thumbs && s.thumbs.length > 0 && (
        <img className="seg-thumb" src={s.thumbs[Math.floor(s.thumbs.length / 2)]} alt="" loading="lazy" draggable={false} />
      )}
      {w > 140 && s.description && <span className="seg-desc">{s.description}</span>}
      <i className="seg-status" />
      {s.score_manual != null && <i className="mark manual" title="Оценка задана вручную" />}
      {s.selected === true && <i className="mark inc" title="Включён вручную" />}
      {s.selected === false && <i className="mark exc" title="Исключён вручную" />}
      {pulse > 0 && <i key={pulse} className="pulse" />}
    </div>
  )
}
