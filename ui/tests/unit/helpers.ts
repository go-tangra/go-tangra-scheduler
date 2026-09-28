import { vi } from 'vitest'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import type { Execution, Task, TaskType } from '@/api/types'

export type Call = { url: string; method: string; body: unknown; headers: Record<string, string> }
export type Reply = { status?: number; body?: unknown }

/** Stubs fetch; the handler sees the path below /api/scheduler/v1/ (query included). */
export function fetchMock(handler: (path: string, method: string, body: unknown) => Reply) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'
    const body = init.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ url, method, body, headers: (init.headers ?? {}) as Record<string, string> })
    const res = handler(url.replace(/^\/api\/scheduler\/v1\//, ''), method, body)
    const status = res.status ?? 200
    return new Response(status === 204 ? null : JSON.stringify(res.body ?? {}), { status, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

export const withAbility = (rules: { action: string; subject: string }[]) => ({ plugins: [[abilitiesPlugin, createMongoAbility(rules), { useGlobalProperties: true }]] as never })

export const READER = [
  { action: 'read', subject: 'SchedulerTask' },
  { action: 'read', subject: 'SchedulerExecution' },
  { action: 'read', subject: 'SchedulerOverview' },
  { action: 'read', subject: 'SchedulerTaskType' },
]
export const OPERATOR = [
  ...READER,
  { action: 'create', subject: 'SchedulerTask' },
  { action: 'update', subject: 'SchedulerTask' },
  { action: 'delete', subject: 'SchedulerTask' },
  { action: 'control', subject: 'SchedulerTask' },
]

export class FakeSource {
  static instances: FakeSource[] = []
  url: string
  closed = false
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners = new Map<string, (e: MessageEvent) => void>()
  constructor(url: string) {
    this.url = url
    FakeSource.instances.push(this)
  }
  addEventListener(t: string, fn: (e: MessageEvent) => void) { this.listeners.set(t, fn) }
  close() { this.closed = true }
  emit(t: string, data: unknown) { this.listeners.get(t)?.({ data: typeof data === 'string' ? data : JSON.stringify(data) } as MessageEvent) }
}

export const mailType: TaskType = {
  name: 'notify:send-mail',
  module: 'notify',
  display_name: 'Send mail',
  description: 'Sends one e-mail.',
  scope: 'tenant',
  available: true,
  default_cron: '0 3 * * *',
  default_max_retries: 2,
  payload_schema: {
    type: 'object',
    required: ['recipient'],
    properties: {
      recipient: { type: 'string', format: 'email', description: 'Who gets the mail.' },
      subject: { type: 'string', default: 'Hello' },
      priority: { type: 'string', enum: ['low', 'normal', 'high'], default: 'normal' },
      copies: { type: 'integer', minimum: 1, maximum: 5 },
      urgent: { type: 'boolean' },
      cc: { type: 'array', items: { type: 'string' } },
    },
  },
}
export const freeType: TaskType = { name: 'ipam:scan', module: 'ipam', display_name: 'Scan network', scope: 'tenant', available: true, payload_schema: null }

export function task(over: Partial<Task> = {}): Task {
  return {
    id: 't1', tenant_id: 'ten', name: 'Nightly mail', type_name: 'notify:send-mail', type_display_name: 'Send mail', module: 'notify',
    kind: 'periodic', payload: { recipient: 'a@example.org' }, cron: '0 3 * * *', timezone: 'UTC', enabled: true, status: 'active',
    state: 'enabled', validity: 'ok', max_retries: 2, run_count: 4, last_status: 'succeeded', last_message: 'sent 1', next_run_at: '2026-09-29T03:00:00Z',
    ...over,
  }
}

export function execution(over: Partial<Execution> = {}): Execution {
  return {
    id: 'e1', task_id: 't1', task_name: 'Nightly mail', occurrence_id: 'o1', type_name: 'notify:send-mail', module: 'notify',
    trigger: 'manual', triggered_by: 'ada', attempt: 1, max_attempts: 3, status: 'queued', ...over,
  }
}
