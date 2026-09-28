import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, describe } from '@/api/client'
import type { Execution, ExecutionCounts, ExecutionFilter, ExecutionPage } from '@/api/types'

export const HISTORY_PAGE_SIZE = 20

/** GET /executions query string (status is a repeatable parameter). */
export function executionQuery(f: ExecutionFilter, page: number, pageSize: number): string {
  const q = new URLSearchParams()
  if (f.task_id) q.set('task_id', f.task_id)
  for (const s of f.status ?? []) q.append('status', s)
  if (f.failed_only) q.set('failed_only', 'true')
  if (f.trigger) q.set('trigger', f.trigger)
  if (f.from) q.set('from', f.from)
  if (f.to) q.set('to', f.to)
  q.set('page', String(page))
  q.set('page_size', String(pageSize))
  return q.toString()
}

// A task's execution history (one row per attempt, newest first).
export const useExecutions = defineStore('scheduler-executions', () => {
  const items = ref<Execution[]>([])
  const total = ref(0)
  const counts = ref<ExecutionCounts>({})
  const page = ref(1)
  const pageSize = ref(HISTORY_PAGE_SIZE)
  const filter = ref<ExecutionFilter>({})
  const loading = ref(false)
  const error = ref('')

  async function list(f: ExecutionFilter = filter.value, p = 1): Promise<void> {
    loading.value = true
    error.value = ''
    filter.value = { ...f }
    try {
      const res = await api<ExecutionPage>('GET', 'executions?' + executionQuery(f, p, pageSize.value))
      items.value = res.items ?? []
      total.value = res.total ?? 0
      counts.value = res.counts ?? {}
      page.value = p
    } catch (e) {
      error.value = describe(e)
    } finally {
      loading.value = false
    }
  }

  const reload = () => list(filter.value, page.value)

  function clear(): void {
    items.value = []
    total.value = 0
    counts.value = {}
    page.value = 1
    filter.value = {}
    error.value = ''
  }

  /** One attempt with its message and result. */
  async function get(id: string): Promise<Execution> {
    return api<Execution>('GET', 'executions/' + id)
  }

  /** A page of attempts without touching the history state (live follow looks for retries). */
  async function fetchPage(f: ExecutionFilter, p = 1, size = 10): Promise<ExecutionPage> {
    return api<ExecutionPage>('GET', 'executions?' + executionQuery(f, p, size))
  }

  return { items, total, counts, page, pageSize, filter, loading, error, list, reload, clear, get, fetchPage }
})
