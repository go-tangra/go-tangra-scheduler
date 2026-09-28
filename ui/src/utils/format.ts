/** A timestamp in the viewer's locale ('' when absent). */
export function when(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** 850 ms, 1.5 s, 2 min 5 s, 1 h 3 min. */
export function duration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return ''
  if (ms < 1000) return `${Math.round(ms)} ms`
  const s = ms / 1000
  if (s < 60) return `${Number(s.toFixed(s < 10 ? 2 : 1))} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min ${Math.round(s - m * 60)} s`
  const h = Math.floor(m / 60)
  return `${h} h ${m - h * 60} min`
}

/** A module result for display: pretty JSON, or the text itself. */
export function resultText(result: unknown): string {
  if (result === undefined || result === null) return ''
  if (typeof result === 'string') return result
  return JSON.stringify(result, null, 2)
}
