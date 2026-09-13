/** H:MM:SS */
export function tc(sec: number): string {
  if (!isFinite(sec) || sec < 0) sec = 0
  const s = Math.floor(sec + 0.5)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = s % 60
  return `${h}:${String(m).padStart(2, '0')}:${String(r).padStart(2, '0')}`
}

/** Compact duration: 1ч 12м, 4м 05с, 37с */
export function dur(sec: number): string {
  const s = Math.round(sec)
  if (s >= 3600) return `${Math.floor(s / 3600)}ч ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}м`
  if (s >= 60) return `${Math.floor(s / 60)}м ${String(s % 60).padStart(2, '0')}с`
  return `${s}с`
}

export function usd(v: number): string {
  if (v >= 10) return `$${v.toFixed(1)}`
  if (v >= 1) return `$${v.toFixed(2)}`
  return `$${v.toFixed(3)}`
}

/**
 * Heat colour for a 1..10 score: cool slate for filler, through amber, to
 * ember red for pivotal moments. Returns [background, foreground].
 */
export function heat(score: number): [string, string] {
  const stops: [number, number, number][] = [
    [62, 72, 84],    // 1  slate
    [70, 84, 100],   // 2
    [82, 100, 118],  // 3
    [120, 108, 90],  // 4  warming
    [160, 118, 72],  // 5
    [190, 124, 58],  // 6  amber
    [214, 120, 46],  // 7
    [226, 104, 40],  // 8
    [220, 78, 38],   // 9  ember
    [205, 52, 40],   // 10
  ]
  const i = Math.min(10, Math.max(1, Math.round(score))) - 1
  const [r, g, b] = stops[i]
  const fg = i >= 5 ? '#14110e' : '#e8ecef'
  return [`rgb(${r},${g},${b})`, fg]
}
