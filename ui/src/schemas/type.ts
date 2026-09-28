import { z } from 'zod'

// Mirrors components.schemas.TaskType of api/openapi/scheduler.yaml. Loose
// objects keep fields a newer server adds.
export const scopeSchema = z.enum(['tenant', 'platform'])

export const taskTypeSchema = z.looseObject({
  name: z.string(),
  module: z.string(),
  display_name: z.string(),
  description: z.string().optional(),
  payload_schema: z.unknown().optional(),
  default_cron: z.string().optional(),
  default_max_retries: z.number().int().optional(),
  scope: scopeSchema,
  available: z.boolean(),
  registered_at: z.string().optional(),
  unregistered_at: z.string().nullable().optional(),
})

export const taskTypeListSchema = z.looseObject({ items: z.array(taskTypeSchema) })
