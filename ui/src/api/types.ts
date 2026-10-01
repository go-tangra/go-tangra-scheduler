// Domain types mirror the scheduler OpenAPI responses (api/openapi/scheduler.yaml).
// Response projections use optional (`?:`) fields; inputs/filters use explicit
// `T | undefined` to satisfy exactOptionalPropertyTypes.

export type Kind = 'periodic' | 'delayed' | 'wait_result'
export type TaskStatus = 'active' | 'completed' | 'cancelled'
export type TaskState = 'enabled' | 'stopped' | 'completed' | 'cancelled'
export type Validity = 'ok' | 'type_unavailable' | 'payload_invalid'
export type Scope = 'tenant' | 'platform'
export type ExecutionStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'timed_out' | 'cancelled'
export type Trigger = 'schedule' | 'manual' | 'catch_up'
export type BulkAction = 'start' | 'stop' | 'restart'

/** A JSON Schema document as registered by a module (null = any object). */
export type JsonSchema = Record<string, unknown>

export interface TaskType {
  name: string
  module: string
  display_name: string
  description?: string
  payload_schema?: JsonSchema | null
  default_cron?: string
  default_max_retries?: number
  scope: Scope
  available: boolean
  registered_at?: string
  unregistered_at?: string | null
}

export interface Task {
  id: string
  tenant_id: string
  name: string
  type_name: string
  type_display_name?: string
  module: string
  scope?: Scope
  kind: Kind
  payload: Record<string, unknown>
  cron?: string
  timezone?: string
  run_at?: string | null
  enabled: boolean
  status: TaskStatus
  state: TaskState
  validity: Validity
  validity_message?: string
  max_retries?: number
  timeout_seconds?: number
  catch_up?: boolean
  remark?: string
  next_run_at?: string | null
  last_run_at?: string | null
  last_status?: string
  last_message?: string
  run_count?: number
  missed_count?: number
  created_by?: string
  updated_by?: string
  created_at?: string
  updated_at?: string
  /** wait_result create only: the first attempt to follow. */
  execution_id?: string
}

/** The list contract fields of a page (go-tangra specs/032-server-side-tables). */
export interface PageInfo {
  total: number
  /** The page returned: a page beyond the end answers the last page. */
  page?: number
  page_size?: number
  sort?: string
  order?: 'asc' | 'desc'
}

/** Page, size and order of a list request. */
export interface ListParams {
  page: number
  page_size: number
  sort: string
  order: 'asc' | 'desc'
}

export interface TaskPage extends PageInfo {
  items: Task[]
}

/** GET /tasks query. */
export interface TaskFilter {
  q?: string | undefined
  kind?: Kind | undefined
  state?: TaskState | undefined
  type?: string | undefined
  validity?: Validity | undefined
}

/** POST /tasks body (unknown fields are refused by the API). */
export interface TaskCreate {
  name: string
  type_name: string
  kind: Kind
  payload?: Record<string, unknown> | undefined
  cron?: string | undefined
  timezone?: string | undefined
  run_at?: string | null | undefined
  delay_seconds?: number | undefined
  enabled?: boolean | undefined
  remark?: string | undefined
  max_retries?: number | undefined
  timeout_seconds?: number | undefined
  catch_up?: boolean | undefined
}

/** PUT /tasks/{id} body: kind and type are immutable. */
export interface TaskUpdate {
  name: string
  payload?: Record<string, unknown> | undefined
  cron?: string | undefined
  timezone?: string | undefined
  run_at?: string | null | undefined
  enabled?: boolean | undefined
  remark?: string | undefined
  max_retries?: number | undefined
  timeout_seconds?: number | undefined
  catch_up?: boolean | undefined
}

export interface Execution {
  id: string
  tenant_id?: string
  task_id: string
  task_name?: string
  occurrence_id: string
  type_name: string
  module: string
  trigger: Trigger
  triggered_by?: string
  scheduled_at?: string
  due_at?: string
  started_at?: string | null
  finished_at?: string | null
  duration_ms?: number
  attempt: number
  max_attempts: number
  status: ExecutionStatus
  message?: string
  /** Module result: a JSON value, or a string when it is not JSON (single execution only). */
  result?: unknown
  result_truncated?: boolean
  /** When the attempt was recorded (the history sort field). */
  created_at?: string
}

export interface ExecutionCounts {
  succeeded?: number
  failed?: number
  other?: number
}

export interface ExecutionPage extends PageInfo {
  items: Execution[]
  counts: ExecutionCounts
}

/** GET /executions query. */
export interface ExecutionFilter {
  task_id?: string | undefined
  status?: ExecutionStatus[] | undefined
  failed_only?: boolean | undefined
  trigger?: Trigger | undefined
  from?: string | undefined
  to?: string | undefined
}

export interface RunAccepted {
  execution_id: string
}

export interface BulkResult {
  affected: number
}

export interface CronPreview {
  next: string[]
}

export interface Overview {
  tasks?: {
    enabled?: number
    stopped?: number
    completed?: number
    cancelled?: number
    type_unavailable?: number
    payload_invalid?: number
  }
  runs_24h?: number
  failures_24h?: number
  next_due?: { task_id?: string; name?: string; next_run_at?: string }[]
  failing?: { task_id?: string; name?: string; last_status?: string; last_message?: string; last_run_at?: string | null }[]
}

/** A `scheduler.execution` event relayed by GET /stream (ids and status only). */
export interface ExecutionEvent {
  execution_id: string
  task_id: string
  status: ExecutionStatus
  attempt?: number
}

/** A `scheduler.task` event relayed by GET /stream. */
export interface TaskEvent {
  task_id: string
}
