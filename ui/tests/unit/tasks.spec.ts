import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { ApiError } from '@/api/client'
import { useTasks } from '@/stores/tasks'
import { useTypes } from '@/stores/types'
import { executionQuery, useExecutions } from '@/stores/executions'
import { useOverview } from '@/stores/overview'
import {
  executionPageSchema, isFinal, statusLabel, taskErrorTarget, taskFormSchema, taskPageSchema, taskTypeListSchema, toCreateBody, toUpdateBody,
} from '@/schemas'
import { execution, fetchMock, freeType, mailType, task } from './helpers'

beforeEach(() => {
  setActivePinia(createPinia())
  document.cookie = '__Host-csrf=tok; Secure; Path=/'
})
afterEach(() => vi.unstubAllGlobals())

const form = (over: Record<string, unknown> = {}) => taskFormSchema.parse({ name: ' Nightly ', type_name: 'notify:send-mail', kind: 'periodic', cron: '0 3 * * *', ...over })

describe('task form schema and request bodies', () => {
  it('periodic: cron required, time zone defaults to UTC, only schedule fields sent', () => {
    expect(taskFormSchema.safeParse({ name: 'x', type_name: 't', kind: 'periodic' }).error?.issues[0]).toMatchObject({ path: ['cron'], message: 'Enter a cron expression.' })
    expect(taskFormSchema.safeParse({ name: '', type_name: '', kind: 'periodic', cron: '* * * * *' }).error?.issues.map((i) => i.path[0])).toEqual(['name', 'type_name'])
    const v = form({ max_retries: 2, timeout_seconds: '', remark: ' note ', catch_up: true })
    expect(toCreateBody(v, { recipient: 'a@b.cd' })).toEqual({
      name: 'Nightly', type_name: 'notify:send-mail', kind: 'periodic', payload: { recipient: 'a@b.cd' }, enabled: true, catch_up: true,
      remark: 'note', max_retries: 2, cron: '0 3 * * *', timezone: 'UTC',
    })
  })

  it('options are bounded (retries 0–10, timeout 1–86400)', () => {
    expect(taskFormSchema.safeParse({ ...form(), max_retries: 11 }).success).toBe(false)
    expect(taskFormSchema.safeParse({ ...form(), max_retries: -1 }).success).toBe(false)
    expect(taskFormSchema.safeParse({ ...form(), timeout_seconds: 0 }).success).toBe(false)
    expect(taskFormSchema.safeParse({ ...form(), timeout_seconds: 1.5 }).success).toBe(false)
    expect(taskFormSchema.parse({ ...form(), max_retries: '3' }).max_retries).toBe(3)
  })

  it('one-shot: run now → delay_seconds 0, after a delay → delay_seconds, at → run_at (UTC)', () => {
    const base = { kind: 'delayed', cron: '' }
    expect(toCreateBody(form({ ...base, when: 'now' }), {})).toMatchObject({ kind: 'delayed', delay_seconds: 0 })
    expect(toCreateBody(form({ ...base, when: 'now' }), {})).not.toHaveProperty('cron')
    expect(toCreateBody(form({ ...base, when: 'delay', delay_amount: 15, delay_unit: 'minutes' }), {})).toMatchObject({ delay_seconds: 900 })
    expect(toCreateBody(form({ ...base, kind: 'wait_result', when: 'delay', delay_amount: '2', delay_unit: 'hours' }), {})).toMatchObject({ kind: 'wait_result', delay_seconds: 7200 })
    const at = toCreateBody(form({ ...base, when: 'at', run_at: '2026-10-01T08:30' }), {})
    expect(at.run_at).toBe(new Date('2026-10-01T08:30').toISOString())
    expect(at).not.toHaveProperty('delay_seconds')
    expect(at).not.toHaveProperty('timezone')
    expect(taskFormSchema.safeParse({ name: 'x', type_name: 't', ...base, when: 'delay' }).error?.issues[0]?.path).toEqual(['delay_amount'])
    expect(taskFormSchema.safeParse({ name: 'x', type_name: 't', ...base, when: 'at', run_at: '' }).error?.issues[0]?.path).toEqual(['run_at'])
    expect(taskFormSchema.safeParse({ name: 'x', type_name: 't', ...base, when: 'delay', delay_amount: 9000, delay_unit: 'hours' }).success).toBe(false)
  })

  it('update: no kind/type; a one-shot delay becomes run_at', () => {
    const now = new Date('2026-09-28T10:00:00Z')
    const periodic = toUpdateBody(form({ timezone: 'Europe/Sofia' }), { a: 1 }, now)
    expect(periodic).toEqual({ name: 'Nightly', payload: { a: 1 }, enabled: true, catch_up: false, remark: '', cron: '0 3 * * *', timezone: 'Europe/Sofia' })
    expect(toUpdateBody(form({ kind: 'delayed', when: 'delay', delay_amount: 30 }), {}, now).run_at).toBe('2026-09-28T10:30:00.000Z')
    expect(toUpdateBody(form({ kind: 'delayed', when: 'at', run_at: '2026-10-01T08:30' }), {}, now).run_at).toBe(new Date('2026-10-01T08:30').toISOString())
  })

  it('maps refusals onto fields and payload pointers', () => {
    expect(taskErrorTarget(new ApiError(422, 'invalid_payload', { field: '/recipient', message: 'not an email' }))).toEqual({ pointer: '/recipient', message: 'not an email' })
    expect(taskErrorTarget(new ApiError(422, 'invalid_payload', { field: 'recipient' }))).toMatchObject({ pointer: '/recipient' })
    expect(taskErrorTarget(new ApiError(422, 'invalid_payload', {}))).toMatchObject({ pointer: '' })
    expect(taskErrorTarget(new ApiError(422, 'payload_too_large', { limit: 65536 }))).toEqual({ pointer: '', message: 'The payload is too large. (limit 65536)' })
    expect(taskErrorTarget(new ApiError(422, 'invalid_cron', { field: 'cron', message: 'expected 5 fields' }))).toEqual({ field: 'cron', message: 'expected 5 fields' })
    expect(taskErrorTarget(new ApiError(422, 'cron_too_frequent', { min_interval_seconds: 60 }))).toEqual({ field: 'cron', message: 'Runs too often: runs must be at least 60 seconds apart.' })
    expect(taskErrorTarget(new ApiError(422, 'cron_too_frequent', {})).field).toBe('cron')
    expect(taskErrorTarget(new ApiError(422, 'invalid_timezone', { field: 'timezone' }))).toEqual({ field: 'timezone', message: 'That is not a known time zone.' })
    expect(taskErrorTarget(new ApiError(422, 'invalid_run_at', {}))).toMatchObject({ field: 'run_at' })
    expect(taskErrorTarget(new ApiError(409, 'name_taken', { field: 'name' }))).toEqual({ field: 'name', message: 'Another task already has that name.' })
    expect(taskErrorTarget(new ApiError(422, 'invalid_options', { field: 'max_retries' }))).toEqual({ field: 'max_retries', message: 'Check the retry and timeout options.' })
    expect(taskErrorTarget(new ApiError(422, 'type_unavailable', { type_name: 'x' }))).toEqual({ message: 'The task type is not available; its module is not registered.' })
    expect(taskErrorTarget(new ApiError(422, 'validation_failed', { message: 'kind' })).message).toBe('Please check the highlighted fields. kind')
    expect(taskErrorTarget(new Error('boom'))).toEqual({ message: 'Something went wrong.' })
  })

  it('response schemas mirror the OpenAPI shapes', () => {
    expect(taskPageSchema.safeParse({ items: [task({ run_at: null, future_field: 1 } as never)], total: 1 }).success).toBe(true)
    expect(taskPageSchema.safeParse({ items: [{ ...task(), state: 'paused' }], total: 1 }).success).toBe(false)
    expect(taskTypeListSchema.safeParse({ items: [mailType, freeType] }).success).toBe(true)
    expect(executionPageSchema.safeParse({ items: [execution({ result: 'text', started_at: null })], total: 1, counts: { succeeded: 1 } }).success).toBe(true)
    expect(executionPageSchema.safeParse({ items: [execution({ status: 'done' as never })], total: 1, counts: {} }).success).toBe(false)
    expect(isFinal('running')).toBe(false)
    expect(isFinal('timed_out')).toBe(true)
    expect(isFinal(undefined)).toBe(false)
    expect(statusLabel('timed_out')).toBe('Timed out')
    expect(statusLabel('weird')).toBe('weird')
    expect(statusLabel(undefined)).toBe('')
  })
})

describe('tasks store', () => {
  it('lists with filter and paging params, skipping blanks', async () => {
    const calls = fetchMock(() => ({ body: { items: [task()], total: 30, page: 2, page_size: 25, sort: 'state', order: 'desc' } }))
    const s = useTasks()
    const res = await s.list({ q: 'mail', kind: 'periodic', state: undefined, type: 'notify:send-mail', validity: 'payload_invalid' }, { page: 2, page_size: 25, sort: 'state', order: 'desc' })
    expect(calls[0]!.url).toBe('/api/scheduler/v1/tasks?q=mail&kind=periodic&type=notify%3Asend-mail&validity=payload_invalid&page=2&page_size=25&sort=state&order=desc')
    expect(s.items.length).toBe(1)
    expect(s.total).toBe(30)
    expect(res?.page).toBe(2)
    expect(s.params).toEqual({ page: 2, page_size: 25, sort: 'state', order: 'desc' })
    // a reload keeps the page, size and order
    await s.reload()
    expect(calls[1]!.url).toBe(calls[0]!.url)
    fetchMock(() => ({ status: 403, body: { reason: 'forbidden' } }))
    await s.list({})
    expect(s.error).toBe('You are not allowed to do that.')
  })

  it('ignores a response superseded by a newer request (rapid paging / sorting)', async () => {
    const resolvers: ((r: Response) => void)[] = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((r) => resolvers.push(r))))
    const reply = (name: string) => new Response(JSON.stringify({ items: [task({ name })], total: 30 }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    const s = useTasks()
    const first = s.list({}, { page: 1, page_size: 25, sort: 'name', order: 'asc' })
    const second = s.list({}, { page: 2, page_size: 25, sort: 'name', order: 'asc' })
    await new Promise((r) => setTimeout(r))
    resolvers[1]!(reply('second'))
    expect((await second)?.items[0]!.name).toBe('second')
    resolvers[0]!(reply('first'))
    expect(await first).toBeNull()
    expect(s.items[0]!.name).toBe('second')
    expect(s.loading).toBe(false)
  })

  it('create posts the body with the CSRF header and returns the wait_result execution id', async () => {
    const calls = fetchMock((path, method, body) => (method === 'POST' ? { status: 201, body: { ...task({ kind: 'wait_result' }), ...(body as object), execution_id: 'e9' } } : { body: {} }))
    const s = useTasks()
    const body = toCreateBody(form({ kind: 'wait_result', when: 'delay', delay_amount: 5 }), { recipient: 'a@b.cd' })
    const t = await s.create(body)
    expect(calls[0]).toMatchObject({ url: '/api/scheduler/v1/tasks', method: 'POST', body: { name: 'Nightly', type_name: 'notify:send-mail', kind: 'wait_result', delay_seconds: 300, payload: { recipient: 'a@b.cd' } } })
    expect(calls[0]!.headers['X-CSRF-Token']).toBe('tok')
    expect(t.execution_id).toBe('e9')
    expect(s.total).toBe(1)
  })

  it('update, delete, control, run, bulk and cron preview hit their routes', async () => {
    const calls = fetchMock((path, method) => {
      if (path === 'tasks/t1' && method === 'DELETE') return { status: 204 }
      if (path.endsWith('/run')) return { status: 202, body: { execution_id: 'e5' } }
      if (path.startsWith('tasks/bulk/')) return { body: { affected: 7 } }
      if (path.startsWith('cron/preview')) return { body: { next: ['2026-09-29T03:00:00Z'] } }
      return { body: task({ state: 'stopped' }) }
    })
    const s = useTasks()
    s.items = [task()]
    s.total = 1
    await s.update('t1', { name: 'x' })
    expect(calls.at(-1)).toMatchObject({ url: '/api/scheduler/v1/tasks/t1', method: 'PUT', body: { name: 'x' } })
    for (const a of ['start', 'stop', 'restart', 'cancel'] as const) {
      await s.control('t1', a)
      expect(calls.at(-1)).toMatchObject({ url: `/api/scheduler/v1/tasks/t1/${a}`, method: 'POST' })
    }
    expect(s.items[0]!.state).toBe('stopped')
    expect(await s.run('t1')).toBe('e5')
    expect(await s.bulk('restart')).toBe(7)
    expect(calls.at(-1)).toMatchObject({ url: '/api/scheduler/v1/tasks/bulk/restart', method: 'POST' })
    expect(await s.previewCron('0 3 * * *', 'Europe/Sofia')).toEqual(['2026-09-29T03:00:00Z'])
    expect(calls.at(-1)!.url).toBe('/api/scheduler/v1/cron/preview?expression=0+3+*+*+*&timezone=Europe%2FSofia&count=5')
    await s.remove('t1')
    expect(calls.at(-1)).toMatchObject({ url: '/api/scheduler/v1/tasks/t1', method: 'DELETE' })
    expect(s.items.length).toBe(0)
    expect(s.total).toBe(0)
  })

  it('surfaces refusals as ApiError with reason and detail', async () => {
    fetchMock(() => ({ status: 422, body: { reason: 'invalid_payload', detail: { field: '/recipient', message: 'bad' } } }))
    const err = await useTasks().create(toCreateBody(form(), {})).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(taskErrorTarget(err)).toEqual({ pointer: '/recipient', message: 'bad' })
  })
})

describe('types, executions and overview stores', () => {
  it('types: loads once (sorted by module then name), force reloads, find by name', async () => {
    const calls = fetchMock(() => ({ body: { items: [mailType, freeType] } }))
    const s = useTypes()
    await s.list()
    await s.list()
    expect(calls.length).toBe(1)
    expect(s.items.map((t) => t.module)).toEqual(['ipam', 'notify'])
    expect(s.find('notify:send-mail')?.default_cron).toBe('0 3 * * *')
    await s.list(true)
    expect(calls.length).toBe(2)
    fetchMock(() => ({ status: 503, body: { reason: 'temporarily_unavailable' } }))
    await s.list(true)
    expect(s.error).toBe('The service is temporarily unavailable.')
  })

  it('executions: repeatable status, failed_only, range and paging; counts kept', async () => {
    expect(executionQuery({ task_id: 't1', status: ['failed', 'timed_out'], failed_only: true, trigger: 'manual', from: 'a', to: 'b' }, { page: 2, page_size: 20, sort: 'duration', order: 'asc' }))
      .toBe('task_id=t1&status=failed&status=timed_out&failed_only=true&trigger=manual&from=a&to=b&page=2&page_size=20&sort=duration&order=asc')
    const calls = fetchMock(() => ({ body: { items: [execution()], total: 41, counts: { succeeded: 30, failed: 10, other: 1 } } }))
    const s = useExecutions()
    await s.list({ task_id: 't1' }, { page: 3, page_size: 10, sort: 'created_at', order: 'desc' })
    expect(calls[0]!.url).toBe('/api/scheduler/v1/executions?task_id=t1&page=3&page_size=10&sort=created_at&order=desc')
    expect(s.counts).toEqual({ succeeded: 30, failed: 10, other: 1 })
    expect(s.total).toBe(41)
    await s.reload()
    expect(calls[1]!.url).toBe(calls[0]!.url)
    await s.fetchPage({ task_id: 't1' })
    expect(calls[2]!.url).toBe('/api/scheduler/v1/executions?task_id=t1&page=1&page_size=10&sort=created_at&order=desc')
    expect(s.params.page).toBe(3)
    await s.get('e1')
    expect(calls[3]!.url).toBe('/api/scheduler/v1/executions/e1')
    s.clear()
    expect(s.items).toEqual([])
    fetchMock(() => ({ status: 404, body: { reason: 'not_found' } }))
    await s.list({ task_id: 'x' })
    expect(s.error).toBe('Not found.')
  })

  it('overview: loads the snapshot; a refusal clears it', async () => {
    fetchMock(() => ({ body: { runs_24h: 5 } }))
    const s = useOverview()
    await s.load()
    expect(s.snapshot?.runs_24h).toBe(5)
    fetchMock(() => ({ status: 403, body: { reason: 'forbidden' } }))
    await s.load()
    expect(s.snapshot).toBeNull()
    expect(s.error).not.toBe('')
  })
})
