import { useEffect, useState } from 'react'
import { api, errText, onDataDirDone, onFFmpegDone, onProgress } from '../api'
import type { ModelInfo, SettingsView } from '../types'

interface Props {
  onClose: () => void
  notify: (t: string, k?: 'info' | 'error') => void
}

function mb(bytes: number): string {
  if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(2)} ГБ`
  if (bytes >= 1 << 20) return `${Math.round(bytes / (1 << 20))} МБ`
  return `${Math.max(1, Math.round(bytes / 1024))} КБ`
}

export default function SettingsDialog({ onClose, notify }: Props) {
  const [s, setS] = useState<SettingsView | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [key, setKey] = useState('')
  const [remember, setRemember] = useState(true)
  const [dl, setDl] = useState<{ done: number; total: number } | null>(null)
  const [size, setSize] = useState<number | null>(null)
  const [newDir, setNewDir] = useState('')
  const [move, setMove] = useState(true)
  const [moving, setMoving] = useState<{ done: number; total: number } | null>(null)

  const load = () => api.getSettings().then(setS).catch((e) => notify(errText(e), 'error'))
  useEffect(() => {
    void load()
    api.models().then(setModels)
    api.dataDirSize().then(setSize).catch(() => setSize(null))
  }, [])
  useEffect(() => {
    const offP = onProgress((e) => {
      if (e.stage === 'ffmpeg') setDl({ done: e.done, total: e.total })
      if (e.stage === 'move') setMoving({ done: e.done, total: e.total })
    })
    const offF = onFFmpegDone((r) => { setDl(null); if (r.error) notify(r.error, 'error'); else notify('ffmpeg установлен'); void load() })
    const offD = onDataDirDone((r) => {
      setMoving(null)
      setNewDir('')
      if (r.error) notify(r.error, 'error')
      else notify(`Папка данных изменена за ${r.elapsed}`)
      void load()
      api.dataDirSize().then(setSize).catch(() => {})
    })
    return () => { offP(); offF(); offD() }
  }, [])

  if (!s) return null
  const busy = s.running || moving != null
  const sourceLabel: Record<string, string> = {
    env: 'задана переменной окружения LONGCUT_DATA_DIR',
    config: 'выбрана в настройках',
    default: 'по умолчанию',
    explicit: '',
  }

  const pickDir = async () => {
    const d = await api.pickDataDir()
    if (d) setNewDir(d)
  }
  const applyDir = async () => {
    try {
      await api.setDataDir(newDir, move)
      setMoving({ done: 0, total: 0 })
    } catch (e) { notify(errText(e), 'error') }
  }

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onClose}>
      <div className="modal settings" onClick={(e) => e.stopPropagation()} role="dialog" aria-label="Настройки">
        <h2>Настройки</h2>

        <div className="field">
          <span className="label">Модель Claude</span>
          <select value={s.model} onChange={async (e) => { await api.setModel(e.target.value); void load() }} disabled={busy}>
            {models.map((m) => <option key={m.id} value={m.id}>{m.id} — ${m.input}/${m.output} за 1M токенов</option>)}
            {!models.some((m) => m.id === s.model) && <option value={s.model}>{s.model}</option>}
          </select>
          <span className="dim small">Смена модели не трогает уже полученные описания; кэш ответов ключуется моделью.</span>
        </div>

        <div className="field">
          <span className="label">Язык описаний</span>
          <select value={s.lang} onChange={async (e) => { await api.setLang(e.target.value); void load() }} disabled={busy}>
            <option value="ru">Русский</option><option value="en">English</option>
          </select>
        </div>

        <div className="field">
          <span className="label">Ключ Anthropic API</span>
          <div className="row">
            <input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder={s.api_key_set ? `задан (${s.api_key_hint})` : 'sk-ant-…'} />
            <button onClick={async () => { try { await api.setAPIKey(key, remember); setKey(''); notify(remember ? 'Ключ сохранён' : 'Ключ применён до закрытия приложения'); void load() } catch (e) { notify(errText(e), 'error') } }} disabled={!key}>Применить</button>
            {s.api_key_set && s.api_key_source !== 'env' && <button onClick={async () => { await api.setAPIKey('', false); void load() }} title="Забыть ключ">Удалить</button>}
          </div>
          <label className="check"><input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} /> Запомнить на этом компьютере</label>
          <span className="dim small">
            {s.api_key_source === 'env' && 'Ключ взят из переменной окружения ANTHROPIC_API_KEY, она имеет приоритет.'}
            {s.api_key_source === 'config' && 'Ключ сохранён в настройках приложения, файл доступен только вашему пользователю.'}
            {s.api_key_source === 'session' && 'Ключ действует до закрытия приложения.'}
            {!s.api_key_set && 'Без ключа описание и оценка не запустятся. Можно также задать переменную окружения ANTHROPIC_API_KEY.'}
          </span>
        </div>

        <div className="field">
          <span className="label">ffmpeg</span>
          {s.ffmpeg_error ? (
            <>
              <span className="error">{s.ffmpeg_error}</span>
              <div className="row">
                <button className="primary" onClick={() => api.downloadFFmpeg()} disabled={dl != null}>Скачать автоматически</button>
                <button onClick={async () => setS(await api.recheckFFmpeg())}>Проверить снова</button>
              </div>
              {dl && <span className="dim small mono">{dl.done} / {dl.total || '?'} МБ</span>}
            </>
          ) : (
            <span className="dim small"><span className="mono">{s.ffmpeg_path}</span><br />{s.ffmpeg_version}</span>
          )}
        </div>

        <div className="field">
          <span className="label">Папка данных</span>
          <div className="row">
            <input value={s.data_dir} readOnly title={s.data_dir} />
            <button onClick={() => api.openDataDir()} title="Открыть в проводнике">Открыть</button>
            <button onClick={pickDir} disabled={busy || s.data_dir_env}>Изменить…</button>
          </div>
          <span className="dim small">
            База проектов, кэш кадров, превью, ffmpeg и экспорт по умолчанию.
            {size != null && <> Сейчас занято <b>{mb(size)}</b>.</>}
            {sourceLabel[s.data_dir_source] && <> Папка {sourceLabel[s.data_dir_source]}.</>}
          </span>
          {s.data_dir_env && <span className="dim small">Пока задана переменная окружения LONGCUT_DATA_DIR, сменить папку из приложения нельзя.</span>}
          {newDir && !moving && (
            <div className="subpanel">
              <div>Новая папка: <span className="mono">{newDir}</span></div>
              <label className="check">
                <input type="checkbox" checked={move} onChange={(e) => setMove(e.target.checked)} />
                Перенести туда существующие данные{size != null && <> ({mb(size)})</>}
              </label>
              {!move && <span className="dim small">Без переноса приложение начнёт с пустой базой в новой папке; старые данные останутся на месте.</span>}
              <div className="row">
                <button className="primary" onClick={applyDir}>Применить</button>
                <button onClick={() => setNewDir('')}>Отмена</button>
              </div>
            </div>
          )}
          {moving && (
            <div className="progress inline">
              <div className="progress-track"><div className="progress-fill" style={{ width: `${moving.total > 0 ? Math.round((moving.done / moving.total) * 100) : 0}%` }} /></div>
              <span className="mono small">{moving.total > 0 ? `${moving.done} / ${moving.total} МБ` : 'перенос…'}</span>
            </div>
          )}
        </div>

        <div className="modal-actions">
          <span className="dim small">Longcut {s.version}</span>
          <div className="spacer" />
          <button onClick={onClose} disabled={busy}>Закрыть</button>
        </div>
      </div>
    </div>
  )
}
