// Plain-words descriptions of common cron schedules ("every day at 03:00",
// "every 15 minutes", "weekdays at 08:00"). Only the usual 5-field patterns
// (minute hour day-of-month month day-of-week) and the @-macros are described;
// anything else falls back to the raw expression. The server stays the
// authority on validity (GET /cron/preview).

const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']
const DAY_NAMES: Record<string, number> = { SUN: 0, MON: 1, TUE: 2, WED: 3, THU: 4, FRI: 5, SAT: 6 }
const MONTH_NAMES: Record<string, number> = { JAN: 1, FEB: 2, MAR: 3, APR: 4, MAY: 5, JUN: 6, JUL: 7, AUG: 8, SEP: 9, OCT: 10, NOV: 11, DEC: 12 }

const MACROS: Record<string, string> = {
  '@yearly': 'every year on 1 January at 00:00',
  '@annually': 'every year on 1 January at 00:00',
  '@monthly': 'on day 1 of every month at 00:00',
  '@weekly': 'every Sunday at 00:00',
  '@daily': 'every day at 00:00',
  '@midnight': 'every day at 00:00',
  '@hourly': 'every hour',
}

const pad = (n: number) => String(n).padStart(2, '0')
const isInt = (s: string) => /^\d+$/.test(s)

/** "Monday, Wednesday and Friday" */
function joinWords(words: string[]): string {
  if (words.length <= 1) return words.join('')
  return words.slice(0, -1).join(', ') + ' and ' + words[words.length - 1]
}

function plural(n: number, unit: string): string {
  return n === 1 ? unit : `${n} ${unit}s`
}

/** Replaces day/month names (MON, jan) with their numbers. */
function numbersFor(field: string, names: Record<string, number>): string {
  return field.toUpperCase().replace(/[A-Z]{3}/g, (m) => (m in names ? String(names[m]) : m))
}

/** Expands a day-of-week field into a sorted set of days (0 = Sunday), or null. */
function weekdays(field: string): number[] | null {
  const out = new Set<number>()
  for (const part of numbersFor(field, DAY_NAMES).split(',')) {
    const range = /^(\d)-(\d)$/.exec(part)
    if (range) {
      const a = Number(range[1])
      const b = Number(range[2])
      if (a > b || b > 7) return null
      for (let d = a; d <= b; d++) out.add(d % 7)
    } else if (/^\d$/.test(part) && Number(part) <= 7) {
      out.add(Number(part) % 7)
    } else {
      return null
    }
  }
  return [...out].sort((x, y) => x - y)
}

/** "weekdays", "weekends", "Monday", "Monday and Friday". */
function daysPhrase(days: number[]): string {
  const key = days.join(',')
  if (key === '1,2,3,4,5') return 'weekdays'
  if (key === '0,6') return 'weekends'
  if (days.length === 7) return 'every day'
  return 'every ' + joinWords(days.map((d) => DAYS[d]!))
}

/** The time-of-day part for fixed minutes/hours: "03:00", "08:00 and 17:00". */
function times(minute: string, hour: string): string | null {
  if (!isInt(minute) || Number(minute) > 59) return null
  const hours = hour.split(',')
  if (!hours.every((h) => isInt(h) && Number(h) <= 23)) return null
  return joinWords(hours.map((h) => `${pad(Number(h))}:${pad(Number(minute))}`))
}

/**
 * Describes a cron expression in plain English, or returns the expression
 * itself when the pattern is not one of the common ones.
 */
export function describeCron(expression: string | undefined | null): string {
  const expr = (expression ?? '').trim()
  if (!expr) return ''
  const lower = expr.toLowerCase()
  if (lower in MACROS) return MACROS[lower]!
  const every = /^@every\s+(.+)$/i.exec(expr)
  if (every) return 'every ' + every[1]!.trim()

  const fields = expr.split(/\s+/)
  if (fields.length !== 5) return expr
  const [minute, hour, dom, month, dowRaw] = fields as [string, string, string, string, string]
  const dow = dowRaw === '?' ? '*' : dowRaw
  const domAny = dom === '*' || dom === '?'

  // Sub-daily patterns: only when every day of every month.
  if (domAny && month === '*' && dow === '*') {
    if (minute === '*' && hour === '*') return 'every minute'
    const stepMin = /^(?:\*|0)\/(\d+)$/.exec(minute)
    if (stepMin && hour === '*') {
      const n = Number(stepMin[1])
      if (n >= 1 && n < 60) return 'every ' + plural(n, 'minute')
    }
    if (isInt(minute) && Number(minute) <= 59 && hour === '*') {
      return Number(minute) === 0 ? 'every hour' : `every hour at minute ${Number(minute)}`
    }
    const stepHour = /^(?:\*|0)\/(\d+)$/.exec(hour)
    if (isInt(minute) && Number(minute) <= 59 && stepHour) {
      const n = Number(stepHour[1])
      if (n >= 1 && n < 24) return 'every ' + plural(n, 'hour') + (Number(minute) ? ` at minute ${Number(minute)}` : '')
    }
  }

  const at = times(minute, hour)
  if (!at) return expr

  if (domAny && month === '*') {
    if (dow === '*') return 'every day at ' + at
    const days = weekdays(dow)
    if (!days) return expr
    return daysPhrase(days) + ' at ' + at
  }

  if (isInt(dom) && Number(dom) >= 1 && Number(dom) <= 31 && dow === '*') {
    if (month === '*') return `on day ${Number(dom)} of every month at ${at}`
    const m = numbersFor(month, MONTH_NAMES)
    if (isInt(m) && Number(m) >= 1 && Number(m) <= 12) return `every year on ${Number(dom)} ${MONTHS[Number(m) - 1]} at ${at}`
  }
  return expr
}

/** A schedule line for a task row: the plain-words cron plus its time zone. */
export function describeSchedule(cron: string | undefined, timezone: string | undefined): string {
  const words = describeCron(cron)
  if (!words) return ''
  return timezone ? `${words} (${timezone})` : words
}

/** Every IANA time zone the browser knows (UTC first), or a short fallback list. */
export function timeZones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] }
  let zones: string[] = []
  try {
    zones = intl.supportedValuesOf ? intl.supportedValuesOf('timeZone') : []
  } catch {
    zones = []
  }
  if (!zones.length) zones = ['Europe/London', 'Europe/Berlin', 'Europe/Sofia', 'America/New_York', 'America/Los_Angeles', 'Asia/Tokyo', 'Australia/Sydney']
  return ['UTC', ...zones.filter((z) => z !== 'UTC')]
}
