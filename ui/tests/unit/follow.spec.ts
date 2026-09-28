import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import ExecutionFollow from '@/components/ExecutionFollow.vue'
import ExecutionDrawer from '@/views/tasks/execution.vue'
import { coalesce, EVENTS, POLL_MS, STREAM_URL, useLive } from '@/stores/live'
import type { Execution } from '@/api/types'
import { FakeSource, execution, fetchMock } from './helpers'

beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('EventSource', FakeSource)
  FakeSource.instances = []
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('live stream store', () => {
  it('shares one reference-counted EventSource and relays valid scheduler events only', () => {
    const live = useLive()
    const r1 = live.connect()
    const r2 = live.connect()
    expect(FakeSource.instances.length).toBe(1)
    const src = FakeSource.instances[0]!
    expect(src.url).toBe(STREAM_URL)
    expect([...src.listeners.keys()]).toEqual([...EVENTS])
    src.onopen?.()
    expect(live.connected).toBe(true)
    const seen: string[] = []
    const off = live.on((e) => seen.push(e.type + ':' + e.data.task_id))
    src.emit('scheduler.execution', { execution_id: 'e1', task_id: 't1', status: 'running', attempt: 1 })
    src.emit('scheduler.task', { task_id: 't2' })
    src.emit('scheduler.execution', { task_id: 't1' }) // no execution id
    src.emit('scheduler.task', 'not json')
    src.emit('scheduler.task', { nope: 1 })
    live._emit('other.event', JSON.stringify({ task_id: 't3' }))
    expect(seen).toEqual(['scheduler.execution:t1', 'scheduler.task:t2'])
    off()
    src.emit('scheduler.task', { task_id: 't4' })
    expect(seen.length).toBe(2)
    src.onerror?.()
    expect(live.connected).toBe(false)
    r1()
    r1()
    expect(src.closed).toBe(false)
    r2()
    expect(src.closed).toBe(true)
  })

  it('without EventSource connecting is a no-op; coalesce runs once per burst', () => {
    vi.stubGlobal('EventSource', undefined)
    const live = useLive()
    live.connect()()
    expect(live.connected).toBe(false)
    vi.useFakeTimers()
    const fn = vi.fn()
    const c = coalesce(fn, 100)
    c.trigger()
    c.trigger()
    vi.advanceTimersByTime(100)
    expect(fn).toHaveBeenCalledTimes(1)
    c.trigger()
    c.cancel()
    vi.advanceTimersByTime(200)
    expect(fn).toHaveBeenCalledTimes(1)
  })
})

/** Serves a sequence of states per execution id (the last one repeats). */
function server(states: Record<string, Partial<Execution>[]>, pages: Execution[][] = []) {
  const served: Record<string, number> = {}
  let pageCall = 0
  const calls = fetchMock((path) => {
    const m = /^executions\/([^?]+)$/.exec(path)
    if (m) {
      const id = m[1]!
      const list = states[id] ?? []
      if (!list.length) return { status: 404, body: { reason: 'not_found' } }
      const i = Math.min(served[id] ?? 0, list.length - 1)
      served[id] = (served[id] ?? 0) + 1
      return { body: execution({ id, ...list[i] }) }
    }
    if (path.startsWith('executions?')) {
      const items = pages[Math.min(pageCall, pages.length - 1)] ?? []
      pageCall++
      return { body: { items, total: items.length, counts: {} } }
    }
    return { status: 404, body: { reason: 'not_found' } }
  })
  return calls
}
const gets = (calls: { url: string }[], id: string) => calls.filter((c) => c.url === '/api/scheduler/v1/executions/' + id).length

describe('ExecutionFollow', () => {
  it('polls every 3 s while queued/running and stops when final, showing message and result', async () => {
    vi.useFakeTimers()
    const calls = server({ e1: [{ status: 'queued' }, { status: 'running', started_at: '2026-09-28T10:00:00Z' }, { status: 'succeeded', message: 'sent 1 mail', result: { sent: 1 }, duration_ms: 1520, finished_at: '2026-09-28T10:00:02Z' }] })
    const w = mount(ExecutionFollow, { props: { executionId: 'e1' }, attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test="execution-status"]').text()).toBe('Queued')
    expect(w.text()).toContain('The result appears when the run finishes.')
    await vi.advanceTimersByTimeAsync(POLL_MS)
    expect(w.find('[data-test="execution-status"]').text()).toBe('Running')
    await vi.advanceTimersByTimeAsync(POLL_MS)
    expect(w.find('[data-test="execution-status"]').text()).toBe('Succeeded')
    expect(w.find('[data-test="execution-message"]').text()).toBe('sent 1 mail')
    expect(JSON.parse(w.find('[data-test="execution-result"]').text())).toEqual({ sent: 1 })
    expect(w.text()).toContain('1.52 s')
    expect(w.text()).toContain('Manual by ada')
    expect(w.emitted('final')?.length).toBe(1)
    const n = gets(calls, 'e1')
    await vi.advanceTimersByTimeAsync(POLL_MS * 5)
    expect(gets(calls, 'e1')).toBe(n)
    w.unmount()
  })

  it('a matching SSE event re-fetches at once; other executions are ignored', async () => {
    vi.useFakeTimers()
    const calls = server({ e1: [{ status: 'running' }, { status: 'failed', attempt: 3, max_attempts: 3, message: 'smtp down', result: 'plain text', result_truncated: true }] })
    const w = mount(ExecutionFollow, { props: { executionId: 'e1' }, attachTo: document.body })
    await flushPromises()
    expect(gets(calls, 'e1')).toBe(1)
    const src = FakeSource.instances[0]!
    src.emit('scheduler.execution', { execution_id: 'other', task_id: 'tX', status: 'succeeded' })
    await flushPromises()
    expect(gets(calls, 'e1')).toBe(1)
    src.emit('scheduler.execution', { execution_id: 'e1', task_id: 't1', status: 'failed', attempt: 3 })
    await flushPromises()
    expect(gets(calls, 'e1')).toBe(2)
    expect(w.find('[data-test="execution-status"]').text()).toBe('Failed')
    expect(w.find('[data-test="execution-result"]').text()).toBe('plain text')
    expect(w.find('[data-test="execution-truncated"]').exists()).toBe(true)
    expect(w.find('[data-test="execution-retry-pending"]').exists()).toBe(false)
    w.unmount()
    expect(src.closed).toBe(true)
  })

  it('follows the retry of a failed attempt (same occurrence) and lists the earlier attempt', async () => {
    vi.useFakeTimers()
    const retry = execution({ id: 'e2', attempt: 2, status: 'queued' })
    const calls = server(
      { e1: [{ status: 'failed', attempt: 1, message: 'timeout' }], e2: [{ status: 'running', attempt: 2 }, { status: 'succeeded', attempt: 2 }] },
      [[execution({ id: 'e1', attempt: 1, status: 'failed' }), execution({ id: 'x', attempt: 2, occurrence_id: 'other' })], [retry, execution({ id: 'e1', attempt: 1, status: 'failed' })]],
    )
    const w = mount(ExecutionFollow, { props: { executionId: 'e1' }, attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test="execution-retry-pending"]').exists()).toBe(true)
    expect(w.emitted('final')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(POLL_MS) // no retry yet
    expect(calls.some((c) => c.url.startsWith('/api/scheduler/v1/executions?task_id=t1'))).toBe(true)
    // the stream announces the retry attempt of the same task
    FakeSource.instances[0]!.emit('scheduler.execution', { execution_id: 'e2', task_id: 't1', status: 'queued', attempt: 2 })
    await flushPromises()
    expect(w.find('[data-test="execution-attempt"]').text()).toBe('Attempt 2 of 3')
    expect(w.find('[data-test="execution-previous"]').text()).toContain('Attempt 1')
    await vi.advanceTimersByTimeAsync(POLL_MS)
    expect(w.find('[data-test="execution-status"]').text()).toBe('Succeeded')
    expect((w.emitted('final')![0]![0] as Execution).id).toBe('e2')
    w.unmount()
  })

  it('opened from the history (followRetries off) a failed attempt stays put; errors are shown', async () => {
    vi.useFakeTimers()
    const calls = server({ e1: [{ status: 'failed', attempt: 1 }] })
    const w = mount(ExecutionFollow, { props: { executionId: 'e1', followRetries: false }, attachTo: document.body })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(POLL_MS * 3)
    expect(calls.length).toBe(1)
    expect(w.emitted('final')?.length).toBe(1)
    w.unmount()

    server({})
    const bad = mount(ExecutionFollow, { props: { executionId: 'missing' }, attachTo: document.body })
    await flushPromises()
    expect(bad.find('[data-test="execution-error"]').text()).toBe('Not found.')
    bad.unmount()
  })

  it('the execution drawer hosts the follow view', async () => {
    server({ e7: [{ status: 'succeeded', message: 'done' }] })
    const w = mount(ExecutionDrawer, { props: { executionId: 'e7' }, attachTo: document.body })
    await flushPromises()
    const drawer = document.body.querySelector('[data-test="execution-drawer"]')!
    expect(drawer.querySelector('[data-test="execution-message"]')!.textContent).toBe('done')
    expect(w.emitted('final')).toBeTruthy()
    await w.setProps({ executionId: null })
    expect(document.body.querySelector('[data-test="execution-drawer"]')).toBeNull()
    w.unmount()
  })
})
