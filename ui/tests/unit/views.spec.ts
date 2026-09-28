import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { axe } from 'vitest-axe'
import { useConfirm } from '@go-tangra/ui'
import Tasks from '@/views/tasks/index.vue'
import TaskDrawer from '@/views/tasks/drawer.vue'
import Overview from '@/views/overview/index.vue'
import type { Task } from '@/api/types'
import { FakeSource, OPERATOR, READER, execution, fetchMock, freeType, mailType, task, withAbility, type Reply } from './helpers'

beforeEach(() => {
  setActivePinia(createPinia())
  document.cookie = '__Host-csrf=tok; Secure; Path=/'
  vi.stubGlobal('EventSource', FakeSource)
  FakeSource.instances = []
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

const q = (sel: string) => document.body.querySelector<HTMLElement>(sel)
const qa = (sel: string) => Array.from(document.body.querySelectorAll<HTMLElement>(sel))
async function setValue(sel: string, value: string, ev = 'input'): Promise<void> {
  const el = q(sel) as HTMLInputElement | HTMLSelectElement
  el.value = value
  el.dispatchEvent(new Event(ev))
  if (ev === 'input' && el instanceof HTMLSelectElement) el.dispatchEvent(new Event('change'))
  await flushPromises()
}
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))

const rows: Task[] = [
  task(),
  task({ id: 't2', name: 'Paused sweep', state: 'stopped', enabled: false, validity: 'type_unavailable', validity_message: 'module ipam is gone', last_status: 'failed', last_message: 'connection refused' }),
  task({ id: 't3', name: 'Send later', kind: 'delayed', cron: '', run_at: '2026-10-01T08:00:00Z', state: 'enabled', status: 'active', last_status: '' }),
  task({ id: 't4', name: 'Sent once', kind: 'delayed', cron: '', state: 'completed', status: 'completed' }),
  task({ id: 't5', name: 'Dropped', kind: 'wait_result', cron: '', state: 'cancelled', status: 'cancelled' }),
]

function api(extra: (path: string, method: string, body: unknown) => Reply | undefined = () => undefined) {
  return (path: string, method: string, body: unknown): Reply => {
    const r = extra(path, method, body)
    if (r) return r
    if (path.startsWith('tasks?')) return { body: { items: rows, total: rows.length } }
    if (path === 'task-types') return { body: { items: [mailType, freeType] } }
    if (path.startsWith('tasks/bulk/')) return { body: { affected: 7 } }
    if (/^tasks\/[^/]+\/run$/.test(path)) return { status: 202, body: { execution_id: 'e5' } }
    if (/^tasks\/[^/]+$/.test(path) && method === 'DELETE') return { status: 204 }
    if (/^tasks\/[^/]+\/\w+$/.test(path)) return { body: task({ state: 'stopped' }) }
    if (path.startsWith('executions/')) return { body: execution({ id: path.split('/')[1]!, status: 'running' }) }
    if (path.startsWith('executions?')) return { body: { items: [execution({ status: 'failed', message: 'smtp down' })], total: 1, counts: { succeeded: 3, failed: 1, other: 0 } } }
    if (path.startsWith('cron/preview')) return { body: { next: ['2026-09-29T03:00:00Z', '2026-09-30T03:00:00Z'] } }
    return { status: 404, body: { reason: 'not_found' } }
  }
}

describe('tasks list', () => {
  it('shows type, schedule in words, next run, state with warnings and last result', async () => {
    const calls = fetchMock(api())
    const w = mount(Tasks, { global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    expect(calls[0]!.url).toBe('/api/scheduler/v1/tasks?page=1&page_size=25')
    const r1 = w.find('[data-test="task-row-t1"]').text()
    expect(r1).toContain('Send mail')
    expect(r1).toContain('notify')
    expect(w.find('[data-test="task-schedule-t1"]').text()).toBe('every day at 03:00 (UTC)')
    expect(w.find('[data-test="task-schedule-t3"]').text()).toContain('once at')
    expect(r1).toContain('Enabled')
    expect(w.find('[data-test="task-validity-t2"]').text()).toBe('Type unavailable')
    expect(w.find('[data-test="task-validity-t2"]').element.closest('[data-tip]')!.getAttribute('data-tip')).toBe('module ipam is gone')
    expect(w.find('[data-test="task-last-t2"]').text()).toBe('Failed')
    expect(w.find('[data-test="task-last-t2"]').element.closest('[data-tip]')!.getAttribute('data-tip')).toBe('connection refused')
    expect(w.find('[data-test="task-row-t1"]').text()).toContain('Succeeded')
    w.unmount()
  })

  it('offers only the actions that fit the task and the permissions', async () => {
    fetchMock(api())
    const w = mount(Tasks, { global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    const has = (sel: string) => w.find(`[data-test="${sel}"]`).exists()
    // periodic enabled
    expect([has('task-stop-t1'), has('task-restart-t1'), has('task-start-t1'), has('task-cancel-t1'), has('task-run-t1')]).toEqual([true, true, false, false, true])
    // periodic stopped
    expect([has('task-start-t2'), has('task-stop-t2')]).toEqual([true, false])
    // one-shot pending
    expect([has('task-cancel-t3'), has('task-start-t3'), has('task-stop-t3'), has('task-run-t3')]).toEqual([true, false, false, true])
    expect(w.find('[data-test="task-run-t3"]').attributes('aria-label')).toBe('Run now')
    // one-shot completed → run again, no cancel
    expect(w.find('[data-test="task-run-t4"]').attributes('aria-label')).toBe('Run again')
    expect(has('task-cancel-t4')).toBe(false)
    // cancelled → nothing to run
    expect(has('task-run-t5')).toBe(false)
    expect(has('task-new') && has('bulk-bar') && has('task-edit-t1') && has('task-delete-t1') && has('task-history-t1')).toBe(true)
    w.unmount()
  })

  it('readers get no write or control actions (history stays)', async () => {
    fetchMock(api())
    const w = mount(Tasks, { global: withAbility(READER), attachTo: document.body })
    await flushPromises()
    for (const sel of ['task-new', 'bulk-bar', 'task-edit-t1', 'task-delete-t1', 'task-stop-t1', 'task-run-t1', 'task-cancel-t3', 'task-start-t2']) {
      expect(w.find(`[data-test="${sel}"]`).exists(), sel).toBe(false)
    }
    expect(w.find('[data-test="task-history-t1"]').exists()).toBe(true)
    w.unmount()

    const only = mount(Tasks, { global: withAbility([{ action: 'read', subject: 'SchedulerTask' }, { action: 'control', subject: 'SchedulerTask' }]), attachTo: document.body })
    await flushPromises()
    expect(only.find('[data-test="task-stop-t1"]').exists()).toBe(true)
    expect(only.find('[data-test="task-history-t1"]').exists()).toBe(false)
    expect(only.find('[data-test="task-edit-t1"]').exists()).toBe(false)
    only.unmount()
  })

  it('filters reload the first page with the chosen values', async () => {
    const calls = fetchMock(api())
    const w = mount(Tasks, { global: withAbility(READER), attachTo: document.body })
    await flushPromises()
    await setValue('#task-filter-kind', 'periodic', 'change')
    expect(calls.at(-1)!.url).toBe('/api/scheduler/v1/tasks?kind=periodic&page=1&page_size=25')
    await setValue('#task-filter-validity', 'payload_invalid', 'change')
    await setValue('#task-filter-q', ' mail ')
    await w.find('#task-filter-q').trigger('keyup', { key: 'Enter' })
    await flushPromises()
    expect(calls.at(-1)!.url).toBe('/api/scheduler/v1/tasks?q=mail&kind=periodic&validity=payload_invalid&page=1&page_size=25')
    w.unmount()
  })

  it('control actions, run now (opens the follow view), cancel/delete ask first, bulk shows the affected count', async () => {
    const calls = fetchMock(api())
    const confirm = useConfirm()
    const w = mount(Tasks, { global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()

    await w.find('[data-test="task-stop-t1"]').trigger('click')
    await flushPromises()
    expect(calls.find((c) => c.url.endsWith('/tasks/t1/stop'))).toMatchObject({ method: 'POST' })

    await w.find('[data-test="task-run-t1"]').trigger('click')
    await flushPromises()
    expect(calls.find((c) => c.url.endsWith('/tasks/t1/run'))).toMatchObject({ method: 'POST' })
    expect(q('[data-test="execution-drawer"]')).not.toBeNull()
    expect(calls.some((c) => c.url === '/api/scheduler/v1/executions/e5')).toBe(true)
    expect(q('[data-test="execution-status"]')!.textContent).toBe('Running')

    await w.find('[data-test="task-cancel-t3"]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.url.endsWith('/tasks/t3/cancel'))).toBe(false)
    confirm.answer(true)
    await flushPromises()
    expect(calls.find((c) => c.url.endsWith('/tasks/t3/cancel'))).toMatchObject({ method: 'POST' })

    await w.find('[data-test="task-delete-t4"]').trigger('click')
    await flushPromises()
    confirm.answer(false)
    await flushPromises()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await w.find('[data-test="task-delete-t4"]').trigger('click')
    await flushPromises()
    confirm.answer(true)
    await flushPromises()
    expect(calls.find((c) => c.method === 'DELETE')!.url).toBe('/api/scheduler/v1/tasks/t4')

    await w.find('[data-test="bulk-stop"]').trigger('click')
    await flushPromises()
    confirm.answer(true)
    await flushPromises()
    expect(calls.find((c) => c.url.endsWith('/tasks/bulk/stop'))).toMatchObject({ method: 'POST' })
    expect(w.find('[data-test="bulk-result"]').text()).toBe('7 tasks stopped.')
    w.unmount()
  })

  it('a refused action shows the reason', async () => {
    fetchMock(api((path) => (path.endsWith('/restart') ? { status: 409, body: { reason: 'not_periodic' } } : undefined)))
    const w = mount(Tasks, { global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    await w.find('[data-test="task-restart-t1"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test="task-error"]').text()).toBe('Only periodic tasks can be started, stopped or restarted.')
    w.unmount()
  })

  it('live events reload the page', async () => {
    const calls = fetchMock(api())
    const w = mount(Tasks, { global: withAbility(READER), attachTo: document.body })
    await flushPromises()
    const before = calls.filter((c) => c.url.includes('/tasks?')).length
    FakeSource.instances[0]!.emit('scheduler.task', { task_id: 't1' })
    await wait(450)
    await flushPromises()
    expect(calls.filter((c) => c.url.includes('/tasks?')).length).toBe(before + 1)
    w.unmount()
  })
})

describe('task drawer', () => {
  it('create: the type pre-fills cron, retries and payload defaults; preview; posts the payload', async () => {
    const calls = fetchMock(api((path, method, body) => (path === 'tasks' && method === 'POST' ? { status: 201, body: task({ ...(body as object) }) } : undefined)))
    const w = mount(TaskDrawer, { props: { open: true, task: null }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    expect(qa('#task-type option').map((o) => o.textContent)).toContain('Send mail (notify)')
    await setValue('#task-type', 'notify:send-mail', 'change')
    expect((q('#task-cron') as HTMLInputElement).value).toBe('0 3 * * *')
    expect((q('#task-retries') as HTMLInputElement).value).toBe('2')
    expect((q('#task-name') as HTMLInputElement).value).toBe('Send mail')
    expect((q('#payload-subject') as HTMLInputElement).value).toBe('Hello')
    expect(q('[data-test="task-type-info"]')!.textContent).toContain('module notify')
    expect(q('[data-test="task-cron"]')!.parentElement!.textContent).toContain('every day at 03:00')
    await wait(450)
    await flushPromises()
    expect(calls.find((c) => c.url.startsWith('/api/scheduler/v1/cron/preview'))!.url).toBe('/api/scheduler/v1/cron/preview?expression=0+3+*+*+*&timezone=UTC&count=5')
    expect(qa('[data-test="cron-preview"] li').length).toBe(2)

    // the required recipient is checked before anything is sent
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    expect(q('[data-test="schema-field-recipient"]')!.textContent).toContain('This field is required.')

    await setValue('#payload-recipient', 'ada@example.org')
    await setValue('#task-timezone', 'Europe/Sofia', 'change')
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({
      name: 'Send mail', type_name: 'notify:send-mail', kind: 'periodic', payload: { recipient: 'ada@example.org', subject: 'Hello', priority: 'normal' },
      enabled: true, catch_up: false, max_retries: 2, cron: '0 3 * * *', timezone: 'Europe/Sofia',
    })
    expect(w.emitted('saved')).toBeTruthy()
    expect(w.emitted('close')).toBeTruthy()
    w.unmount()
  })

  it('shows the preview error reason from the API', async () => {
    fetchMock(api((path) => (path.startsWith('cron/preview') ? { status: 422, body: { reason: 'invalid_cron', detail: { field: 'cron', message: 'expected exactly 5 fields' } } } : undefined)))
    const w = mount(TaskDrawer, { props: { open: true, task: null }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    await setValue('#task-cron', '0 3 * *')
    await wait(450)
    await flushPromises()
    expect(q('[data-test="cron-preview-error"]')!.textContent).toBe('expected exactly 5 fields')
    w.unmount()
  })

  it('maps 422 errors onto the payload field (JSON pointer) and the cron field', async () => {
    let reply: Reply = { status: 422, body: { reason: 'invalid_payload', detail: { field: '/recipient', message: 'mailbox unknown' } } }
    fetchMock(api((path, method) => (path === 'tasks' && method === 'POST' ? reply : undefined)))
    const w = mount(TaskDrawer, { props: { open: true, task: null }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    await setValue('#task-type', 'notify:send-mail', 'change')
    await setValue('#payload-recipient', 'ada@example.org')
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(q('[data-test="schema-field-recipient"]')!.textContent).toContain('mailbox unknown')
    expect(w.emitted('close')).toBeFalsy()

    reply = { status: 422, body: { reason: 'cron_too_frequent', detail: { min_interval_seconds: 300 } } }
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(q('[data-test="task-cron"]')!.closest('div')!.parentElement!.textContent).toContain('at least 300 seconds apart')

    reply = { status: 422, body: { reason: 'type_unavailable', detail: {} } }
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(q('[data-test="task-form-error"]')!.textContent).toContain('not available')
    w.unmount()
  })

  it('wait-for-result: run now sends delay_seconds 0 and opens the follow view; delay/at bodies', async () => {
    const calls = fetchMock(api((path, method, body) => (path === 'tasks' && method === 'POST' ? { status: 201, body: task({ ...(body as object), execution_id: 'e9' }) } : undefined)))
    const w = mount(TaskDrawer, { props: { open: true, task: null }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    await setValue('#task-type', 'ipam:scan', 'change')
    await setValue('#task-kind', 'wait_result', 'change')
    expect(q('#task-cron')).toBeNull()
    expect(q('[data-test="schema-form-fallback"]')).not.toBeNull()
    await setValue('#payload-json', '{"subnet": "10.0.0.0/24"}')
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ name: 'Scan network', type_name: 'ipam:scan', kind: 'wait_result', payload: { subnet: '10.0.0.0/24' }, enabled: true, catch_up: false, delay_seconds: 0 })
    expect(w.emitted('follow')![0]).toEqual(['e9'])
    w.unmount()

    const calls2 = fetchMock(api((path, method, body) => (path === 'tasks' && method === 'POST' ? { status: 201, body: task({ ...(body as object) }) } : undefined)))
    const d = mount(TaskDrawer, { props: { open: true, task: null }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    await setValue('#task-type', 'ipam:scan', 'change')
    await setValue('#task-kind', 'delayed', 'change')
    await setValue('#task-when', 'delay', 'change')
    await setValue('#task-delay', '2')
    await setValue('#task-delay-unit', 'hours', 'change')
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls2.find((c) => c.method === 'POST')!.body).toMatchObject({ kind: 'delayed', delay_seconds: 7200 })
    expect(d.emitted('follow')).toBeFalsy()
    d.unmount()
  })

  it('edit: kind and type are fixed, PUT sends the editable fields; the History tab lists attempts', async () => {
    const calls = fetchMock(api((path, method, body) => (path === 't1' || (path === 'tasks/t1' && method === 'PUT') ? { body: task({ ...(body as object) }) } : undefined)))
    const w = mount(TaskDrawer, { props: { open: true, task: task() }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    expect(q('select#task-type')).toBeNull()
    expect((q('#task-kind') as HTMLInputElement).readOnly).toBe(true)
    expect((q('#payload-recipient') as HTMLInputElement).value).toBe('a@example.org')
    await setValue('#task-name', 'Renamed')
    ;(q('[data-test="task-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'PUT')).toMatchObject({
      url: '/api/scheduler/v1/tasks/t1',
      body: { name: 'Renamed', payload: { recipient: 'a@example.org' }, enabled: true, catch_up: false, remark: '', max_retries: 2, cron: '0 3 * * *', timezone: 'UTC' },
    })
    w.unmount()

    const h = mount(TaskDrawer, { props: { open: true, task: task(), initialTab: 'history' }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    expect(calls.at(-1)!.url).toBe('/api/scheduler/v1/executions?task_id=t1&page=1&page_size=20')
    expect(q('[data-test="history-counts"]')!.textContent).toContain('3 succeeded')
    expect(q('[data-test="history-counts"]')!.textContent).toContain('1 failed')
    expect(q('[data-test="task-save"]')).toBeNull()
    ;(q('#history-failed-only') as HTMLInputElement).click()
    await flushPromises()
    expect(calls.at(-1)!.url).toBe('/api/scheduler/v1/executions?task_id=t1&failed_only=true&page=1&page_size=20')
    await setValue('#history-status', 'timed_out', 'change')
    expect(calls.at(-1)!.url).toContain('status=timed_out')
    await setValue('#history-from', '2026-09-01T00:00')
    ;(q('[data-test="history-apply"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.at(-1)!.url).toContain('from=' + encodeURIComponent(new Date('2026-09-01T00:00').toISOString()))
    ;(q('[data-test="execution-row-e1"]') as HTMLElement).click()
    await flushPromises()
    expect(h.emitted('open-execution')![0]).toEqual(['e1'])
    h.unmount()
  })

  it('without update permission the task is shown read-only; without execution read there is no History tab', async () => {
    fetchMock(api())
    const w = mount(TaskDrawer, { props: { open: true, task: task() }, global: withAbility([{ action: 'read', subject: 'SchedulerTask' }]), attachTo: document.body })
    await flushPromises()
    expect(q('[data-test="task-details"]')!.textContent).toContain('every day at 03:00 (UTC)')
    expect(q('[data-test="task-save"]')).toBeNull()
    expect(q('[data-test="task-form"]')).toBeNull()
    expect(document.body.textContent).not.toContain('History')
    w.unmount()
  })
})

describe('overview', () => {
  it('shows counts, 24 h figures, next runs and failing tasks linking to the task list', async () => {
    fetchMock(() => ({
      body: {
        tasks: { enabled: 4, stopped: 1, completed: 2, cancelled: 0, type_unavailable: 1, payload_invalid: 3 },
        runs_24h: 48, failures_24h: 2,
        next_due: [{ task_id: 't1', name: 'Nightly mail', next_run_at: '2026-09-29T03:00:00Z' }],
        failing: [{ task_id: 't2', name: 'Paused sweep', last_status: 'timed_out', last_message: 'no answer', last_run_at: '2026-09-28T02:00:00Z' }],
      },
    }))
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/scheduler', component: { render: () => null } }, { path: '/scheduler/dashboard', component: Overview }] })
    await router.push('/scheduler/dashboard')
    const w = mount(Overview, { global: { plugins: [router, withAbility(READER).plugins[0]] as never }, attachTo: document.body })
    await flushPromises()
    const tiles = w.find('[data-test="overview-tasks"]').text()
    for (const s of ['Enabled', '4', 'Stopped', 'Completed', 'Type unavailable', 'Payload invalid', '3']) expect(tiles).toContain(s)
    expect(w.find('[data-test="overview-runs"]').text()).toContain('48')
    expect(w.find('[data-test="overview-next-t1"]').attributes('href')).toBe('/scheduler?q=Nightly+mail')
    expect(w.find('[data-test="overview-failing-t2"]').text()).toBe('Paused sweep')
    expect(w.find('[data-test="overview-failing"]').text()).toContain('Timed out')
    expect(w.find('[data-test="overview-failing"]').text()).toContain('no answer')
    w.unmount()

    fetchMock(() => ({ body: {} }))
    const empty = mount(Overview, { global: { plugins: [router] as never }, attachTo: document.body })
    await flushPromises()
    expect(empty.text()).toContain('Nothing scheduled')
    expect(empty.text()).toContain('No failing tasks')
    empty.unmount()
  })

  it('the task list takes ?q= from the overview link', async () => {
    const calls = fetchMock(api())
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/scheduler', component: Tasks }] })
    await router.push('/scheduler?q=Nightly')
    const w = mount(Tasks, { global: { plugins: [router, withAbility(READER).plugins[0]] as never }, attachTo: document.body })
    await flushPromises()
    expect(calls[0]!.url).toBe('/api/scheduler/v1/tasks?q=Nightly&page=1&page_size=25')
    w.unmount()
  })
})

describe('accessibility', () => {
  const rules = { rules: { 'heading-order': { enabled: false }, 'color-contrast': { enabled: false }, region: { enabled: false } } }

  it('the task list has no axe violations and no inline styles', async () => {
    fetchMock(api())
    const list = mount(Tasks, { global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    expect(document.body.querySelector('[style]')).toBeNull()
    expect((await axe(list.element as HTMLElement, rules)).violations).toEqual([])
    list.unmount()
  }, 30_000)

  it('the task drawer form has no axe violations', async () => {
    fetchMock(api())
    const d = mount(TaskDrawer, { props: { open: true, task: task() }, global: withAbility(OPERATOR), attachTo: document.body })
    await flushPromises()
    expect(document.body.querySelector('[style]')).toBeNull()
    // aria-allowed-role: the kit's UiDrawer renders <aside role="dialog"> (kit markup, best-practice rule).
    const drawerRules = { rules: { ...rules.rules, 'aria-allowed-role': { enabled: false } } }
    expect((await axe(q('[data-test="task-drawer"]')!, drawerRules)).violations).toEqual([])
    d.unmount()
  }, 60_000)

  it('the overview has no axe violations', async () => {
    fetchMock(() => ({ body: { tasks: { enabled: 1 }, next_due: [], failing: [] } }))
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)', component: { render: () => null } }] })
    const o = mount(Overview, { global: { plugins: [router] as never }, attachTo: document.body })
    await flushPromises()
    expect((await axe(o.element as HTMLElement, rules)).violations).toEqual([])
    o.unmount()
  }, 30_000)
})
