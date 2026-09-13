import { useEffect, useState } from 'react'
import { api, errText } from '../api'
import type { ProjectSummary } from '../types'
import { dur, usd } from '../format'

interface Props {
  current: string | null
  onOpen: (name: string) => void
  onCreate: (name: string, game: string) => void
  onClose: () => void
  onError: (m: string) => void
}

export default function ProjectPicker({ current, onOpen, onCreate, onClose, onError }: Props) {
  const [projects, setProjects] = useState<ProjectSummary[] | null>(null)
  const [name, setName] = useState('')
  const [game, setGame] = useState('')

  const load = () => api.listProjects().then(setProjects).catch((e) => onError(errText(e)))
  useEffect(() => { void load() }, [])

  const remove = async (n: string) => {
    if (!confirm(`Удалить проект «${n}»? Описания и оценки будут потеряны (кэш ответов API останется).`)) return
    try { await api.deleteProject(n); await load() } catch (e) { onError(errText(e)) }
  }

  return (
    <div className="modal-backdrop" onClick={current ? onClose : undefined}>
      <div className="modal picker" onClick={(e) => e.stopPropagation()} role="dialog" aria-label="Проекты">
        <h2>Проекты</h2>
        {projects == null && <p className="dim">Загрузка…</p>}
        {projects && projects.length === 0 && <p className="dim">Проектов пока нет. Создайте первый ниже.</p>}
        {projects && projects.length > 0 && (
          <table className="projects">
            <thead><tr><th>Название</th><th>Игра</th><th>Файлы</th><th>Длительность</th><th>Сегменты</th><th>Потрачено</th><th>Изменён</th><th></th></tr></thead>
            <tbody>
              {projects.map((p) => (
                <tr key={p.name} className={p.name === current ? 'current' : ''} onDoubleClick={() => onOpen(p.name)}>
                  <td><button className="link" onClick={() => onOpen(p.name)}>{p.name}</button></td>
                  <td>{p.game || <span className="dim">—</span>}</td>
                  <td className="mono">{p.videos}</td>
                  <td className="mono">{dur(p.duration)}</td>
                  <td className="mono">{p.scored}/{p.segments}</td>
                  <td className="mono">{usd(p.spent_usd)}</td>
                  <td className="dim">{p.updated}</td>
                  <td><button className="icon danger" title="Удалить проект" onClick={() => remove(p.name)}>🗑</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <form className="new-project" onSubmit={(e) => { e.preventDefault(); if (name.trim()) onCreate(name.trim(), game.trim()) }}>
          <h3>Новый проект</h3>
          <div className="row">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Название, например kenshi-s1" autoFocus />
            <input value={game} onChange={(e) => setGame(e.target.value)} placeholder="Игра (для промптов), например Kenshi" />
            <button className="primary" type="submit" disabled={!name.trim()}>Создать</button>
          </div>
        </form>
        {current && <div className="modal-actions"><button onClick={onClose}>Закрыть</button></div>}
      </div>
    </div>
  )
}
