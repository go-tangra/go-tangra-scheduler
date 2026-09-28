// The scheduler API through the gateway: the kit client bound to this module's base.
// Mutating calls carry the platform CSRF header (X-CSRF-Token, double-submit
// cookie) — the kit client adds it to every non-GET request.
import { createApi, ApiError, csrfToken, describe, type Method, type RequestOptions } from '@go-tangra/ui/api'
import { registerReasons } from '@go-tangra/ui/forms'
import type { paths } from './schema.d'

export { ApiError, csrfToken, describe }
export type { Method, RequestOptions }

// Path names are checked against the OpenAPI contract at compile time.
export type ApiPath = keyof paths
export const BASE = '/api/scheduler/v1'

// Scheduler-specific refusal reasons (closed vocabulary, api/openapi/scheduler.yaml).
registerReasons({
  invalid_payload: 'The payload does not match the task type.',
  payload_too_large: 'The payload is too large.',
  payload_too_deep: 'The payload is nested too deeply.',
  invalid_cron: 'That is not a valid cron expression.',
  cron_too_frequent: 'The schedule runs too often.',
  invalid_timezone: 'That is not a known time zone.',
  invalid_options: 'Check the retry and timeout options.',
  invalid_run_at: 'Choose a run time in the future.',
  type_unavailable: 'The task type is not available; its module is not registered.',
  name_taken: 'Another task already has that name.',
  not_periodic: 'Only periodic tasks can be started, stopped or restarted.',
  run_in_progress: 'The task is already running.',
  not_cancellable: 'The task has already finished and cannot be cancelled.',
  invalid_backup: 'The backup file is not valid.',
})

export const api = createApi({ base: BASE })
