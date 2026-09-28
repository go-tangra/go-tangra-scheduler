import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { ExecutionEvent, TaskEvent } from '@/api/types'

// One shared EventSource relays this tenant's scheduler events (GET /stream,
// scheduler:read) through the gateway. It is reference-counted so the task
// list and an execution follow view share a connection. Events carry ids and
// status only (never payloads or results). The module closes the stream at
// 290 s; EventSource reconnects on its own (sending Last-Event-ID).
export type SchedulerEvent =
  | { type: 'scheduler.execution'; data: ExecutionEvent }
  | { type: 'scheduler.task'; data: TaskEvent }
export type Listener = (e: SchedulerEvent) => void

export const EVENTS = ['scheduler.execution', 'scheduler.task'] as const
export const STREAM_URL = '/api/scheduler/v1/stream'
/** Poll interval of a run that is not final (the stream may be silent). */
export const POLL_MS = 3000

export const useLive = defineStore('scheduler-live', () => {
  const connected = ref(false)
  let source: EventSource | null = null
  let refs = 0
  const listeners = new Set<Listener>()

  function handle(type: string, raw: string): void {
    let data: unknown
    try {
      data = JSON.parse(raw)
    } catch {
      return // non-JSON frames are ignored
    }
    if (!data || typeof data !== 'object' || typeof (data as { task_id?: unknown }).task_id !== 'string') return
    let event: SchedulerEvent
    if (type === 'scheduler.execution') {
      if (typeof (data as { execution_id?: unknown }).execution_id !== 'string') return
      event = { type, data: data as ExecutionEvent }
    } else if (type === 'scheduler.task') {
      event = { type, data: data as TaskEvent }
    } else {
      return
    }
    for (const l of listeners) l(event)
  }

  function open(): void {
    if (source || typeof EventSource === 'undefined') return
    source = new EventSource(STREAM_URL, { withCredentials: true })
    source.onopen = () => (connected.value = true)
    source.onerror = () => (connected.value = false)
    for (const t of EVENTS) source.addEventListener(t, (e) => handle(t, (e as MessageEvent).data))
  }

  function close(): void {
    refs = 0
    source?.close()
    source = null
    connected.value = false
  }

  /** Opens the stream (first caller) and returns a release function. */
  function connect(): () => void {
    refs += 1
    open()
    let released = false
    return () => {
      if (released) return
      released = true
      refs -= 1
      if (refs <= 0) close()
    }
  }

  /** Subscribes to every event; returns the unsubscribe function. */
  function on(l: Listener): () => void {
    listeners.add(l)
    return () => listeners.delete(l)
  }

  return { connected, connect, close, on, _emit: handle }
})

/**
 * Calls fn at most once per `wait` ms for a burst of events (a bulk restart
 * emits many); the returned cancel stops a pending call.
 */
export function coalesce(fn: () => void, wait = 400): { trigger: () => void; cancel: () => void } {
  let timer: ReturnType<typeof setTimeout> | null = null
  return {
    trigger: () => {
      if (timer) return
      timer = setTimeout(() => {
        timer = null
        fn()
      }, wait)
    },
    cancel: () => {
      if (timer) clearTimeout(timer)
      timer = null
    },
  }
}
