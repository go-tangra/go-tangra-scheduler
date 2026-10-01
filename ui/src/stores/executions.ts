import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { ListQueryOptions } from '@go-tangra/ui'
import { api, describe } from '@/api/client'
import type { Execution, ExecutionCounts, ExecutionFilter, ExecutionPage, ListParams } from '@/api/types'

export const HISTORY_PAGE_SIZE = 25

/** Sortable fields of GET /executions (server Spec store.ExecutionList). */
export const RUN_SORTS = ['created_at', 'status', 'duration', 'trigger'] as const
export const RUN_LIST: ListQueryOptions = { sortable: [...RUN_SORTS], defaultSort: { key: 'created_at', dir: 'desc' }, defaultSize: HISTORY_PAGE_SIZE }
const FIRST_PAGE: ListParams = { page: 1, page_size: HISTORY_PAGE_SIZE, sort: 'created_at', order: 'desc' }

/** GET /executions query string (status is a repeatable parameter). */
export function executionQuery(f: ExecutionFilter, q: ListParams): string {
  const s = new URLSearchParams()
  if (f.task_id) s.set('task_id', f.task_id)
  for (const st of f.status ?? []) s.append('status', st)
  if (f.failed_only) s.set('failed_only', 'true')
  if (f.trigger) s.set('trigger', f.trigger)
  if (f.from) s.set('from', f.from)
  if (f.to) s.set('to', f.to)
  s.set('page', String(q.page))
  s.set('page_size', String(q.page_size))
  s.set('sort', q.sort)
  s.set('order', q.order)
  return s.toString()
}

// A task's execution history (one row per attempt, newest first by default).
export const useExecutions = defineStore('scheduler-executions', () => {
  const items = ref<Execution[]>([])
  const total = ref(0)
  const counts = ref<ExecutionCounts>({})
  const params = ref<ListParams>({ ...FIRST_PAGE })
  const filter = ref<ExecutionFilter>({})
  const loading = ref(false)
  const error = ref('')
  let seq = 0

  /** Loads one server page; null when it failed or a newer request superseded it. */
  async function list(f: ExecutionFilter = filter.value, q: ListParams = params.value): Promise<ExecutionPage | null> {
    const mine = ++seq
    loading.value = true
    error.value = ''
    filter.value = { ...f }
    params.value = { ...q }
    try {
      const res = await api<ExecutionPage>('GET', 'executions?' + executionQuery(f, q))
      if (mine !== seq) return null
      items.value = res.items ?? []
      total.value = res.total ?? 0
      counts.value = res.counts ?? {}
      return res
    } catch (e) {
      if (mine === seq) error.value = describe(e)
      return null
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** Reloads the current page with the current filter and order. */
  const reload = () => list()

  function clear(): void {
    items.value = []
    total.value = 0
    counts.value = {}
    params.value = { ...FIRST_PAGE }
    filter.value = {}
    error.value = ''
  }

  /** One attempt with its message and result. */
  async function get(id: string): Promise<Execution> {
    return api<Execution>('GET', 'executions/' + id)
  }

  /** A page of attempts, newest first, without touching the history state (live follow looks for retries). */
  async function fetchPage(f: ExecutionFilter, p = 1, size = 10): Promise<ExecutionPage> {
    return api<ExecutionPage>('GET', 'executions?' + executionQuery(f, { page: p, page_size: size, sort: 'created_at', order: 'desc' }))
  }

  return { items, total, counts, params, filter, loading, error, list, reload, clear, get, fetchPage }
})
