import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { ListQueryOptions } from '@go-tangra/ui'
import { api, describe } from '@/api/client'
import type { BulkAction, BulkResult, CronPreview, ListParams, RunAccepted, Task, TaskCreate, TaskFilter, TaskPage, TaskUpdate } from '@/api/types'

export const PAGE_SIZE = 25

/** Sortable fields of GET /tasks (server Spec store.TaskList). */
export const TASK_SORTS = ['name', 'type', 'state', 'next_run_at', 'updated_at'] as const
export const TASK_LIST: ListQueryOptions = { sortable: [...TASK_SORTS], defaultSort: { key: 'name', dir: 'asc' }, defaultSize: PAGE_SIZE }
const FIRST_PAGE: ListParams = { page: 1, page_size: PAGE_SIZE, sort: 'name', order: 'asc' }

export type ControlAction = 'start' | 'stop' | 'restart' | 'cancel'

export const useTasks = defineStore('scheduler-tasks', () => {
  const items = ref<Task[]>([])
  const total = ref(0)
  const params = ref<ListParams>({ ...FIRST_PAGE })
  const filter = ref<TaskFilter>({})
  const loading = ref(false)
  const error = ref('')
  let seq = 0

  /**
   * Loads one server page with the filter (blank filter values are not sent).
   * Resolves with the page, or null when it failed or a newer request
   * superseded it (its rows are then ignored).
   */
  async function list(f: TaskFilter = filter.value, q: ListParams = params.value): Promise<TaskPage | null> {
    const mine = ++seq
    loading.value = true
    error.value = ''
    filter.value = { ...f }
    params.value = { ...q }
    try {
      const res = await api<TaskPage>('GET', 'tasks', undefined, { query: { ...f, ...q } })
      if (mine !== seq) return null
      items.value = res.items ?? []
      total.value = res.total ?? 0
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

  function replace(t: Task): Task {
    items.value = items.value.map((x) => (x.id === t.id ? t : x))
    return t
  }

  async function get(id: string): Promise<Task> {
    return api<Task>('GET', 'tasks/' + id)
  }

  /** Creates the task; a wait_result task comes back with the execution_id to follow. */
  async function create(body: TaskCreate): Promise<Task> {
    const t = await api<Task>('POST', 'tasks', body)
    items.value = [t, ...items.value]
    total.value += 1
    return t
  }

  async function update(id: string, body: TaskUpdate): Promise<Task> {
    return replace(await api<Task>('PUT', 'tasks/' + id, body))
  }

  async function remove(id: string): Promise<void> {
    await api('DELETE', 'tasks/' + id)
    const before = items.value.length
    items.value = items.value.filter((x) => x.id !== id)
    if (items.value.length < before) total.value = Math.max(0, total.value - 1)
  }

  /** start / stop / restart (periodic) or cancel (one-shot not finished). */
  async function control(id: string, action: ControlAction): Promise<Task> {
    return replace(await api<Task>('POST', `tasks/${id}/${action}`))
  }

  /** Run now / run again: queues a manual attempt and returns its id. */
  async function run(id: string): Promise<string> {
    const res = await api<RunAccepted>('POST', `tasks/${id}/run`)
    return res.execution_id
  }

  /** Start, stop or restart every periodic task in the caller's scope. */
  async function bulk(action: BulkAction): Promise<number> {
    const res = await api<BulkResult>('POST', 'tasks/bulk/' + action)
    return res.affected ?? 0
  }

  /** The next run times of a cron expression (server-side parser). */
  async function previewCron(expression: string, timezone: string, count = 5, signal?: AbortSignal): Promise<string[]> {
    const res = await api<CronPreview>('GET', 'cron/preview', undefined, { query: { expression, timezone, count }, ...(signal ? { signal } : {}) })
    return res.next ?? []
  }

  return { items, total, params, filter, loading, error, list, reload, get, create, update, remove, control, run, bulk, previewCron, replace }
})
