import { useEffect } from 'react'

export default function Toast({ text, kind, onHide }: { text: string; kind: 'info' | 'error'; onHide: () => void }) {
  useEffect(() => {
    const t = window.setTimeout(onHide, kind === 'error' ? 9000 : 4000)
    return () => window.clearTimeout(t)
  }, [text, kind])
  return (
    <div className={`toast ${kind}`} role={kind === 'error' ? 'alert' : 'status'} onClick={onHide}>
      {text}
    </div>
  )
}
