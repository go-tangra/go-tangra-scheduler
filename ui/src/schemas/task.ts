import { z } from 'zod'
import { ApiError, describe } from '@/api/client'
import type { Kind, TaskCreate, TaskState, TaskUpdate, Validity } from '@/api/types'
import { scopeSchema } from './type'

// Mirrors components.schemas.Task / TaskPage / TaskCreate / TaskUpdate of
// api/openapi/scheduler.yaml, plus the task drawer's form schema.

export const KINDS = ['periodic', 'delayed', 'wait_result'] as const satisfies readonly Kind[]
export const TASK_STATES = ['enabled', 'stopped', 'completed', 'cancelled'] as const satisfies readonly TaskState[]
export const VALIDITIES = ['ok', 'type_unavailable', 'payload_invalid'] as const satisfies readonly Validity[]

export const kindSchema = z.enum(KINDS)
export const taskStateSchema = z.enum(TASK_STATES)
export const validitySchema = z.enum(VALIDITIES)

export const taskSchema = z.looseObject({
  id: z.string(),
  tenant_id: z.string(),
  name: z.string(),
  type_name: z.string(),
  type_display_name: z.string().optional(),
  module: z.string(),
  scope: scopeSchema.optional(),
  kind: kindSchema,
  payload: z.record(z.string(), z.unknown()),
  cron: z.string().optional(),
  timezone: z.string().optional(),
  run_at: z.string().nullable().optional(),
  enabled: z.boolean(),
  status: z.enum(['active', 'completed', 'cancelled']),
  state: taskStateSchema,
  validity: validitySchema,
  validity_message: z.string().optional(),
  max_retries: z.number().int().optional(),
  timeout_seconds: z.number().int().optional(),
  catch_up: z.boolean().optional(),
  remark: z.string().optional(),
  next_run_at: z.string().nullable().optional(),
  last_run_at: z.string().nullable().optional(),
  last_status: z.string().optional(),
  last_message: z.string().optional(),
  run_count: z.number().int().optional(),
  missed_count: z.number().int().optional(),
  created_by: z.string().optional(),
  updated_by: z.string().optional(),
  created_at: z.string().optional(),
  updated_at: z.string().optional(),
  execution_id: z.string().optional(),
})

export const taskPageSchema = z.looseObject({
  items: z.array(taskSchema),
  total: z.number().int(),
  page: z.number().int().optional(),
  page_size: z.number().int().optional(),
  sort: z.string().optional(),
  order: z.enum(['asc', 'desc']).optional(),
})

// --- labels ---
export const KIND_LABELS: Record<Kind, string> = { periodic: 'Periodic', delayed: 'Delayed', wait_result: 'Wait for result' }
export const STATE_LABELS: Record<TaskState, string> = { enabled: 'Enabled', stopped: 'Stopped', completed: 'Completed', cancelled: 'Cancelled' }
export const STATE_COLORS: Record<TaskState, 'success' | 'neutral' | 'info' | 'warning'> = { enabled: 'success', stopped: 'neutral', completed: 'info', cancelled: 'warning' }
export const VALIDITY_LABELS: Record<Validity, string> = { ok: 'Valid', type_unavailable: 'Type unavailable', payload_invalid: 'Payload invalid' }

export const isOneShot = (k: Kind) => k !== 'periodic'

// --- the drawer form ---
export const WHEN_MODES = ['now', 'delay', 'at'] as const
export type WhenMode = (typeof WHEN_MODES)[number]
export const DELAY_UNITS = ['minutes', 'hours'] as const
export type DelayUnit = (typeof DELAY_UNITS)[number]

const optionalInt = (min: number, max: number) =>
  z.preprocess((v) => (v === '' || v === null || v === undefined || (typeof v === 'number' && Number.isNaN(v)) ? undefined : v), z.coerce.number().int('Enter a whole number.').min(min).max(max).optional())

/**
 * The task drawer's options (the payload is validated separately against the
 * type's JSON Schema). Output feeds toCreateBody / toUpdateBody.
 */
export const taskFormSchema = z.object({
  name: z.string().trim().min(1, 'Enter a name.').max(200),
  type_name: z.string().min(1, 'Choose a task type.'),
  kind: kindSchema,
  cron: z.string().trim().max(128).default(''),
  timezone: z.string().trim().max(64).default('UTC'),
  when: z.enum(WHEN_MODES).default('now'),
  delay_amount: optionalInt(1, 525_600),
  delay_unit: z.enum(DELAY_UNITS).default('minutes'),
  run_at: z.string().default(''),
  max_retries: optionalInt(0, 10),
  timeout_seconds: optionalInt(1, 86_400),
  catch_up: z.boolean().default(false),
  enabled: z.boolean().default(true),
  remark: z.string().trim().max(1000).default(''),
}).superRefine((v, ctx) => {
  if (v.kind === 'periodic' && !v.cron) ctx.addIssue({ code: 'custom', path: ['cron'], message: 'Enter a cron expression.' })
  if (v.kind !== 'periodic') {
    if (v.when === 'delay' && v.delay_amount === undefined) ctx.addIssue({ code: 'custom', path: ['delay_amount'], message: 'Enter how long to wait.' })
    if (v.when === 'delay' && v.delay_amount !== undefined && v.delay_amount * (v.delay_unit === 'hours' ? 3600 : 60) > 31_536_000) ctx.addIssue({ code: 'custom', path: ['delay_amount'], message: 'The delay must be at most one year.' })
    if (v.when === 'at' && Number.isNaN(Date.parse(v.run_at))) ctx.addIssue({ code: 'custom', path: ['run_at'], message: 'Choose the date and time to run.' })
  }
})
export type TaskFormInput = z.input<typeof taskFormSchema>
export type TaskFormOutput = z.output<typeof taskFormSchema>

/** Seconds of a delay form value. */
export const delaySeconds = (amount: number, unit: DelayUnit) => amount * (unit === 'hours' ? 3600 : 60)

/** POST /tasks body of a validated form: only the fields of the task's kind. */
export function toCreateBody(v: TaskFormOutput, payload: Record<string, unknown>): TaskCreate {
  const body: TaskCreate = { name: v.name, type_name: v.type_name, kind: v.kind, payload, enabled: v.enabled, catch_up: v.catch_up }
  if (v.remark) body.remark = v.remark
  if (v.max_retries !== undefined) body.max_retries = v.max_retries
  if (v.timeout_seconds !== undefined) body.timeout_seconds = v.timeout_seconds
  if (v.kind === 'periodic') {
    body.cron = v.cron
    body.timezone = v.timezone || 'UTC'
  } else if (v.when === 'delay' && v.delay_amount !== undefined) {
    body.delay_seconds = delaySeconds(v.delay_amount, v.delay_unit)
  } else if (v.when === 'at') {
    body.run_at = new Date(v.run_at).toISOString()
  } else {
    body.delay_seconds = 0
  }
  return body
}

/**
 * PUT /tasks/{id} body (kind and type are immutable). A one-shot "after
 * delay" becomes an absolute run_at: the update has no delay_seconds.
 */
export function toUpdateBody(v: TaskFormOutput, payload: Record<string, unknown>, now: Date = new Date()): TaskUpdate {
  const body: TaskUpdate = { name: v.name, payload, enabled: v.enabled, catch_up: v.catch_up, remark: v.remark }
  if (v.max_retries !== undefined) body.max_retries = v.max_retries
  if (v.timeout_seconds !== undefined) body.timeout_seconds = v.timeout_seconds
  if (v.kind === 'periodic') {
    body.cron = v.cron
    body.timezone = v.timezone || 'UTC'
  } else if (v.when === 'delay' && v.delay_amount !== undefined) {
    body.run_at = new Date(now.getTime() + delaySeconds(v.delay_amount, v.delay_unit) * 1000).toISOString()
  } else if (v.when === 'at') {
    body.run_at = new Date(v.run_at).toISOString()
  }
  return body
}

/** Where a refusal belongs in the task drawer. */
export interface TaskErrorTarget {
  /** A form option (name, cron, timezone, run_at, max_retries, timeout_seconds). */
  field?: string
  /** A payload JSON pointer (`/recipient`); '' for the payload as a whole. */
  pointer?: string
  message: string
}

const FORM_FIELDS = new Set(['name', 'cron', 'timezone', 'run_at', 'max_retries', 'timeout_seconds', 'type_name'])

/** Maps a create/update refusal (422 detail.field) onto the drawer. */
export function taskErrorTarget(e: unknown): TaskErrorTarget {
  const base = describe(e)
  if (!(e instanceof ApiError)) return { message: base }
  const d = e.detail ?? {}
  const field = typeof d.field === 'string' ? d.field : ''
  const detailMessage = typeof d.message === 'string' && d.message ? d.message : ''
  switch (e.reason) {
    case 'invalid_payload':
      return { pointer: field.startsWith('/') ? field : field ? '/' + field : '', message: detailMessage || base }
    case 'payload_too_large':
    case 'payload_too_deep':
      return { pointer: '', message: base + (typeof d.limit === 'number' ? ` (limit ${d.limit})` : '') }
    case 'cron_too_frequent': {
      const n = typeof d.min_interval_seconds === 'number' ? d.min_interval_seconds : undefined
      return { field: 'cron', message: n ? `Runs too often: runs must be at least ${n} seconds apart.` : base }
    }
    case 'invalid_cron':
      return { field: 'cron', message: detailMessage || base }
    case 'invalid_timezone':
      return { field: 'timezone', message: base }
    case 'invalid_run_at':
      return { field: 'run_at', message: detailMessage || base }
    case 'name_taken':
      return { field: 'name', message: base }
    default:
      if (field && FORM_FIELDS.has(field)) return { field, message: detailMessage || base }
      return { message: detailMessage ? base + ' ' + detailMessage : base }
  }
}
