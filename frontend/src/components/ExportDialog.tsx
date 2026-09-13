import { useEffect, useState } from 'react'
import { api, errText, onExportDone, onProgress } from '../api'
import type { PlanView, ProjectView, ProgressEvent } from '../types'
import { dur, tc } from '../format'

interface Props {
  project: ProjectView
  threshold: number
  onClose: () => void
  notify: (t: string, k?: 'info' | 'error') => void
}

type Target = 'video' | 'lists'

export default function ExportDialog({ project, threshold, onClose, notify }: Props) {
  const [plan, setPlan] = useState<PlanView | null>(null)
  const [target, setTarget] = useState<Target>('video')
  const [out, setOut] = useState('')
  const [encode, setEncode] = useState(false)
  const [height, setHeight] = useState(0)
  const [edl, setEdl] = useState(true)
  const [xml, setXml] = useState(true)
  const [running, setRunning] = useState(false)
  const [ev, setEv] = useState<ProgressEvent | null>(null)
  const [done, setDone] = useState<string | null>(null)

  useEffect(() => { api.exportPlan(threshold).then(setPlan).catch((e) => notify(errText(e), 'error')) }, [threshold])
  useEffect(() => {
    const offP = onProgress((e) => { if (e.stage === 'render') setEv(e) })
    const offD = onExportDone((r) => {
      setRunning(false)
      if (r.error) notify(r.error, 'error')
      else { setDone(out); notify(target === 'lists' ? 'EDL/XML сохранены' : `Экспорт готов за ${r.elapsed}`) }
    })
    return () => { offP(); offD() }
  }, [out, target])

  // Switching the target changes the sensible default file name.
  const switchTarget = (t: Target) => {
    setTarget(t)
    setOut('')
    setDone(null)
    if (t === 'lists' && !edl && !xml) setEdl(true)
  }

  const pick = async () => {
    const p = target === 'lists'
      ? await api.pickExportPath(`${project.name}_cut.edl`, 'edl')
      : await api.pickExportPath(`${project.name}_cut.mp4`, 'mp4')
    if (p) setOut(p)
  }
  const start = async () => {
    try {
      setDone(null)
      await api.export({ threshold, out, encode: target === 'video' && encode, height: target === 'video' && encode ? height : 0, edl, xml, no_video: target === 'lists' })
      setRunning(true)
    } catch (e) { notify(errText(e), 'error') }
  }
  const pct = ev && ev.total > 0 ? Math.round((ev.done / ev.total) * 100) : 0
  const listsChosen = edl || xml
  const canStart = !running && !!out && !!plan && plan.clips > 0 && (target === 'video' || listsChosen)

  return (
    <div className="modal-backdrop" onClick={running ? undefined : onClose}>
      <div className="modal export" onClick={(e) => e.stopPropagation()} role="dialog" aria-label="Экспорт">
        <h2>Экспорт</h2>
        {plan && (
          <p>
            Порог <b>{threshold}</b>: <b>{plan.clips}</b> клип(ов), <b>{dur(plan.duration)}</b> из {dur(project.duration)}.
            {plan.clips === 0 && <span className="error"> Нечего экспортировать, понизьте порог.</span>}
          </p>
        )}
        {plan && plan.items.length > 0 && (
          <div className="plan">
            {plan.items.map((c, i) => (
              <div key={i} className="plan-row">
                <span className="mono dim">{String(i + 1).padStart(3, ' ')}</span>
                <span className="mono">{tc(c.start)}–{tc(c.end)}</span>
                <span className="dim">{c.video}</span>
                <span className="plan-label" title={c.label}>{c.label}</span>
              </div>
            ))}
          </div>
        )}

        <div className="field">
          <span className="label">Что экспортировать</span>
          <div className="segmented">
            <button className={target === 'video' ? 'on' : ''} onClick={() => switchTarget('video')} disabled={running}>Видео + списки</button>
            <button className={target === 'lists' ? 'on' : ''} onClick={() => switchTarget('lists')} disabled={running}>Только EDL / XML</button>
          </div>
          {target === 'lists' && <span className="dim small">Видео не рендерится. Списки ссылаются на исходные файлы по имени, монтажка соберёт таймлайн из них сама.</span>}
        </div>

        <div className="field">
          <span className="label">{target === 'lists' ? 'Файл (расширение подставится по формату)' : 'Файл'}</span>
          <div className="row">
            <input value={out} readOnly placeholder={target === 'lists' ? 'Куда сохранить .edl / .xml' : 'Куда сохранить .mp4'} />
            <button onClick={pick} disabled={running}>Выбрать…</button>
          </div>
        </div>

        {target === 'video' && (
          <div className="field">
            <span className="label">Режим</span>
            <label className="check"><input type="radio" checked={!encode} onChange={() => setEncode(false)} /> Без перекодирования (мгновенно, без потерь; срезы по ключевым кадрам)</label>
            <label className="check"><input type="radio" checked={encode} onChange={() => setEncode(true)} /> Перекодировать libx264 CRF 18 (точно по кадру, медленно)</label>
            {encode && (
              <label className="check indent">Уменьшить до
                <select value={height} onChange={(e) => setHeight(Number(e.target.value))}>
                  <option value={0}>оригинал</option><option value={1440}>1440p</option><option value={1080}>1080p</option><option value={720}>720p</option>
                </select>
              </label>
            )}
          </div>
        )}

        <div className="field">
          <span className="label">{target === 'lists' ? 'Форматы' : 'Для монтажки'}</span>
          <label className="check"><input type="checkbox" checked={edl} onChange={(e) => setEdl(e.target.checked)} /> EDL (CMX 3600){target === 'video' && ' рядом с файлом'}</label>
          <label className="check"><input type="checkbox" checked={xml} onChange={(e) => setXml(e.target.checked)} /> XML (Final Cut Pro 7) для Premiere Pro и DaVinci Resolve</label>
          {target === 'lists' && !listsChosen && <span className="error small">Выберите хотя бы один формат.</span>}
        </div>

        {running && target === 'video' && (
          <div className="progress inline">
            <div className="progress-track"><div className="progress-fill" style={{ width: `${pct}%` }} /></div>
            <span className="mono">{ev ? `${tc(ev.done)} / ${tc(ev.total)}` : 'запуск…'}</span>
            <button className="danger" onClick={() => api.cancel()}>Стоп</button>
          </div>
        )}
        {done && <p className="ok">Сохранено: <span className="mono">{done}</span> <button className="link" onClick={() => api.showInFolder(done)}>открыть папку</button></p>}
        <div className="modal-actions">
          <button onClick={onClose} disabled={running}>Закрыть</button>
          <button className="primary" onClick={start} disabled={!canStart}>{target === 'lists' ? 'Сохранить списки' : 'Экспортировать'}</button>
        </div>
      </div>
    </div>
  )
}
