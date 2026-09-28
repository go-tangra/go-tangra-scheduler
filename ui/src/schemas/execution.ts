import { z } from 'zod'
import type { ExecutionStatus, Trigger } from '@/api/types'

// Mirrors components.schemas.Execution / ExecutionPage of api/openapi/scheduler.yaml.
export const EXECUTION_STATUSES = ['queued', 'running', 'succeeded', 'failed', 'skipped', 'timed_out', 'cancelled'] as const satisfies readonly ExecutionStatus[]
export const TRIGGERS = ['schedule', 'manual', 'catch_up'] as const satisfies readonly Trigger[]

export const executionStatusSchema = z.enum(EXECUTION_STATUSES)
export const triggerSchema = z.enum(TRIGGERS)

export const executionSchema = z.looseObject({
  id: z.string(),
  tenant_id: z.string().optional(),
  task_id: z.string(),
  task_name: z.string().optional(),
  occurrence_id: z.string(),
  type_name: z.string(),
  module: z.string(),
  trigger: triggerSchema,
  triggered_by: z.string().optional(),
  scheduled_at: z.string().optional(),
  due_at: z.string().optional(),
  started_at: z.string().nullable().optional(),
  finished_at: z.string().nullable().optional(),
  duration_ms: z.number().int().optional(),
  attempt: z.number().int(),
  max_attempts: z.number().int(),
  status: executionStatusSchema,
  message: z.string().optional(),
  result: z.unknown().optional(),
  result_truncated: z.boolean().optional(),
})

export const executionPageSchema = z.looseObject({
  items: z.array(executionSchema),
  total: z.number().int(),
  counts: z.looseObject({ succeeded: z.number().int().optional(), failed: z.number().int().optional(), other: z.number().int().optional() }),
})

/** A run that will not change any more. */
export const FINAL_STATUSES: readonly ExecutionStatus[] = ['succeeded', 'failed', 'skipped', 'timed_out', 'cancelled']
export const isFinal = (s: string | undefined): boolean => !!s && (FINAL_STATUSES as readonly string[]).includes(s)

export const EXECUTION_STATUS_LABELS: Record<ExecutionStatus, string> = {
  queued: 'Queued',
  running: 'Running',
  succeeded: 'Succeeded',
  failed: 'Failed',
  skipped: 'Skipped',
  timed_out: 'Timed out',
  cancelled: 'Cancelled',
}
export const EXECUTION_STATUS_COLORS: Record<ExecutionStatus, 'neutral' | 'info' | 'primary' | 'success' | 'error' | 'warning'> = {
  queued: 'neutral',
  running: 'info',
  succeeded: 'success',
  failed: 'error',
  skipped: 'neutral',
  timed_out: 'warning',
  cancelled: 'neutral',
}
export const TRIGGER_LABELS: Record<Trigger, string> = { schedule: 'Schedule', manual: 'Manual', catch_up: 'Catch-up' }

/** The label of any status string (the task's last_status is a free string). */
export function statusLabel(s: string | undefined): string {
  if (!s) return ''
  return EXECUTION_STATUS_LABELS[s as ExecutionStatus] ?? s
}
