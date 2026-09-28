import { describe, expect, it, vi } from 'vitest'
import { describeCron, describeSchedule, timeZones } from '@/utils/cron'

describe('describeCron', () => {
  it.each([
    ['* * * * *', 'every minute'],
    ['*/15 * * * *', 'every 15 minutes'],
    ['0/1 * * * *', 'every minute'],
    ['0 * * * *', 'every hour'],
    ['5 * * * *', 'every hour at minute 5'],
    ['0 */2 * * *', 'every 2 hours'],
    ['30 */6 * * *', 'every 6 hours at minute 30'],
    ['0 3 * * *', 'every day at 03:00'],
    ['0 8,17 * * *', 'every day at 08:00 and 17:00'],
    ['0 8 * * 1-5', 'weekdays at 08:00'],
    ['0 8 * * MON-FRI', 'weekdays at 08:00'],
    ['30 9 * * 0,6', 'weekends at 09:30'],
    ['0 6 * * 1', 'every Monday at 06:00'],
    ['0 6 * * 1,3,5', 'every Monday, Wednesday and Friday at 06:00'],
    ['0 6 * * 7', 'every Sunday at 06:00'],
    ['0 6 * * 0-6', 'every day at 06:00'],
    ['0 0 1 * *', 'on day 1 of every month at 00:00'],
    ['15 4 25 12 *', 'every year on 25 December at 04:15'],
    ['0 0 1 jan *', 'every year on 1 January at 00:00'],
    ['@daily', 'every day at 00:00'],
    ['@hourly', 'every hour'],
    ['@weekly', 'every Sunday at 00:00'],
    ['@every 90m', 'every 90m'],
  ])('%s → %s', (expr, words) => {
    expect(describeCron(expr)).toBe(words)
  })

  it.each(['0 3 1-7 * 1', '*/7 3 * * *', '0 25 * * *', '1,2 3 * * *', '0 3 * * 8', '0 3 L * *', 'a b c', '0 3 * * * *'])('falls back to the raw expression: %s', (expr) => {
    expect(describeCron(expr)).toBe(expr)
  })

  it('blank is empty; schedule adds the time zone', () => {
    expect(describeCron('')).toBe('')
    expect(describeCron(undefined)).toBe('')
    expect(describeSchedule('0 3 * * *', 'Europe/Sofia')).toBe('every day at 03:00 (Europe/Sofia)')
    expect(describeSchedule('0 3 * * *', undefined)).toBe('every day at 03:00')
    expect(describeSchedule(undefined, 'UTC')).toBe('')
  })

  it('time zones start with UTC and fall back without Intl.supportedValuesOf', () => {
    const zones = timeZones()
    expect(zones[0]).toBe('UTC')
    expect(zones.filter((z) => z === 'UTC').length).toBe(1)
    const spy = vi.spyOn(Intl as unknown as { supportedValuesOf: (k: string) => string[] }, 'supportedValuesOf').mockImplementation(() => {
      throw new Error('unsupported')
    })
    expect(timeZones()).toContain('Europe/Berlin')
    spy.mockRestore()
  })
})
