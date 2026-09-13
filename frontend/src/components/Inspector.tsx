import { useEffect, useRef, useState } from 'react'
import { api, errText } from '../api'
import type { ProjectView, SegmentView } from '../types'
import { effectiveScore, isKept } from '../types'
import { dur, heat, tc } from '../format'

interface Props {
  segment: SegmentView | null
  project: ProjectView | null
  threshold: number
  onManualScore: (id: number, score: number | null) => void
  onSelected: (id: number, sel: boolean | null) => void
  onError: (msg: string) => void
}

export default function Inspector({ segment, project, threshold, onManualScore, onSelected, onError }: Props) {
  const [src, setSrc] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const videoRef = useRef<HTMLVideoElement>(null)
  const reqId = useRef(0)

  // Load (and lazily render) the preview proxy when the selection changes.
  useEffect(() => {
    setSrc('')
    if (!segment) return
    const id = ++reqId.current
    setLoading(true)
    api.proxyURL(segment.id)
      .then((u) => { if (reqId.current === id) setSrc(u) })
      .catch((e) => { if (reqId.current === id) onError('Превью: ' + errText(e)) })
      .finally(() => { if (reqId.current === id) setLoading(false) })
  }, [segment?.id])

  if (!segment || !project) {
    return (
      <aside className="inspector">
        <div className="empty-hint">
          <p>Выберите сегмент на таймлайне.</p>
          <p className="dim">Клавиши: ← → переход, 1–9 и 0 ручная оценка, Del сброс, I включить, X исключить.</p>
        </div>
      </aside>
    )
  }
  const video = project.videos.find((v) => v.id === segment.video_id)
  const score = effectiveScore(segment)
  const scored = segment.score != null || segment.score_manual != null
  const [bg, fg] = scored ? heat(score) : ['var(--panel-2)', 'var(--ink-3)']
  const kept = isKept(segment, threshold)

  return (
    <aside className="inspector">
      <div className="preview">
        {src ? (
          <video ref={videoRef} src={src} controls autoPlay muted={false} playsInline />
        ) : (
          <div className="preview-placeholder">
            {segment.thumbs && segment.thumbs.length > 0 && <img src={segment.thumbs[Math.floor(segment.thumbs.length / 2)]} alt="" />}
            <span>{loading ? 'Готовлю превью…' : 'Нет превью'}</span>
          </div>
        )}
      </div>
      <div className="insp-head">
        <div className="insp-title">
          <span className="idx">#{segment.idx}</span>
          <span className="mono">{tc(segment.start)} – {tc(segment.end)}</span>
          <span className="dim">{dur(segment.end - segment.start)}</span>
        </div>
        <div className="dim small" title={video?.path}>{video?.name}</div>
      </div>

      <div className="insp-score">
        <div className="score-big" style={{ background: bg, color: fg }}>{scored ? score : '–'}</div>
        <div className="score-meta">
          <div className={`kept ${kept ? 'yes' : 'no'}`}>{kept ? 'В монтаже' : 'Вне монтажа'}{segment.selected != null && ' (вручную)'}</div>
          {segment.score != null && segment.score_manual != null && segment.score !== segment.score_manual && (
            <div className="dim small">оценка модели {segment.score}</div>
          )}
          <div className="dim small">{statusLabel(segment.status)} · движение {segment.motion_avg.toFixed(3)}</div>
        </div>
      </div>

      <div className="insp-block">
        <div className="label">Ручная оценка</div>
        <div className="score-row">
          {Array.from({ length: 10 }, (_, i) => i + 1).map((n) => {
            const [b, f] = heat(n)
            const active = segment.score_manual === n
            return (
              <button key={n} className={`score-btn ${active ? 'active' : ''}`} style={{ '--h': b, '--hf': f } as React.CSSProperties}
                onClick={() => onManualScore(segment.id, active ? null : n)} title={`Поставить ${n}`}>{n}</button>
            )
          })}
          <button className="score-btn clear" onClick={() => onManualScore(segment.id, null)} disabled={segment.score_manual == null} title="Вернуть оценку модели">×</button>
        </div>
      </div>

      <div className="insp-block">
        <div className="label">В монтаж</div>
        <div className="segmented">
          <button className={segment.selected == null ? 'on' : ''} onClick={() => onSelected(segment.id, null)}>По порогу</button>
          <button className={segment.selected === true ? 'on' : ''} onClick={() => onSelected(segment.id, true)}>Включить</button>
          <button className={segment.selected === false ? 'on' : ''} onClick={() => onSelected(segment.id, false)}>Исключить</button>
        </div>
      </div>

      <div className="insp-block grow">
        <div className="label">Описание</div>
        {segment.description ? <p className="desc">{segment.description}</p> : <p className="dim">Ещё не описан. Запустите «Описание».</p>}
        {segment.score_reason && (
          <>
            <div className="label">Почему такая оценка</div>
            <p className="reason">{segment.score_reason}</p>
          </>
        )}
        {segment.error && <p className="error">Ошибка: {segment.error}</p>}
      </div>

      {segment.thumbs && segment.thumbs.length > 0 && (
        <div className="thumbs">
          {segment.thumbs.map((t, i) => (
            <img key={t} src={t} alt={`кадр ${i + 1}`} loading="lazy"
              onClick={() => { const v = videoRef.current; if (v && segment.thumbs) { v.currentTime = ((i + 0.5) / segment.thumbs.length) * (segment.end - segment.start); v.play().catch(() => {}) } }} />
          ))}
        </div>
      )}
    </aside>
  )
}

function statusLabel(s: string): string {
  switch (s) {
    case 'detected': return 'нарезан'
    case 'framed': return 'кадры извлечены'
    case 'described': return 'описан'
    case 'scored': return 'оценён'
    default: return s
  }
}
