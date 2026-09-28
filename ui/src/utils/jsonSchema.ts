// JSON Schema → form fields for a task type's payload (research D12).
// Supported: an object schema whose properties are string (plain, enum,
// format email / uuid / date-time / date), integer / number (minimum /
// maximum), boolean and array of strings. Anything else (nested objects,
// $ref, combinators, other arrays, free-form objects, a null schema) makes the
// form fall back to the raw JSON editor. The server validates the payload
// again; its errors carry a JSON pointer (`/recipient`) mapped back to a field.

import type { JsonSchema } from '@/api/types'

export type SchemaFieldKind = 'text' | 'textarea' | 'email' | 'uuid' | 'datetime' | 'date' | 'select' | 'integer' | 'number' | 'boolean' | 'string-list'

export interface SchemaField {
  key: string
  /** JSON pointer of the value (`/recipient`). */
  pointer: string
  kind: SchemaFieldKind
  label: string
  description?: string
  required: boolean
  options?: string[]
  minimum?: number
  maximum?: number
  minLength?: number
  maxLength?: number
  pattern?: string
  minItems?: number
  maxItems?: number
  default?: unknown
}

export interface SchemaForm {
  /** False when the schema needs the raw JSON editor. */
  supported: boolean
  /** Why the generated form is not available (shown next to the JSON editor). */
  reason?: string
  fields: SchemaField[]
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)
const COMBINATORS = ['$ref', 'oneOf', 'anyOf', 'allOf', 'not', 'if', 'patternProperties', 'dependentSchemas', 'dependencies']

/** Escapes a property name for a JSON pointer segment (RFC 6901). */
export function pointerOf(key: string): string {
  return '/' + key.replace(/~/g, '~0').replace(/\//g, '~1')
}

/** The top-level property a JSON pointer addresses (`/tags/0` → `tags`), '' for the root. */
export function pointerToKey(pointer: string): string {
  const p = pointer.startsWith('#') ? pointer.slice(1) : pointer
  if (!p || p === '/') return ''
  const first = p.replace(/^\//, '').split('/')[0] ?? ''
  return first.replace(/~1/g, '/').replace(/~0/g, '~')
}

/** "max_retries" → "Max retries", "recipientEmail" → "Recipient email". */
export function humanize(key: string): string {
  const words = key.replace(/([a-z0-9])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').trim().toLowerCase()
  return words ? words[0]!.toUpperCase() + words.slice(1) : key
}

/** The single type of a property: `"string"` or `["string","null"]` → string. */
function typeOf(p: Record<string, unknown>): string | null {
  const t = p.type
  if (typeof t === 'string') return t
  if (Array.isArray(t)) {
    const rest = t.filter((x) => x !== 'null')
    return rest.length === 1 && typeof rest[0] === 'string' ? rest[0] : null
  }
  if (Array.isArray(p.enum) && p.enum.every((x) => typeof x === 'string')) return 'string'
  return null
}

const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined)

function fieldOf(key: string, p: Record<string, unknown>, required: boolean): SchemaField | string {
  for (const c of COMBINATORS) if (c in p) return `"${key}" uses ${c}`
  const type = typeOf(p)
  const base = {
    key,
    pointer: pointerOf(key),
    label: typeof p.title === 'string' && p.title ? p.title : humanize(key),
    required,
    ...(typeof p.description === 'string' && p.description ? { description: p.description } : {}),
    ...('default' in p ? { default: p.default } : {}),
  }
  switch (type) {
    case 'string': {
      const strs = {
        ...(num(p.minLength) !== undefined ? { minLength: num(p.minLength)! } : {}),
        ...(num(p.maxLength) !== undefined ? { maxLength: num(p.maxLength)! } : {}),
        ...(typeof p.pattern === 'string' ? { pattern: p.pattern } : {}),
      }
      if (Array.isArray(p.enum)) {
        if (!p.enum.every((x) => typeof x === 'string')) return `"${key}" has a non-text choice list`
        return { ...base, kind: 'select', options: p.enum as string[] }
      }
      switch (p.format) {
        case 'email': return { ...base, ...strs, kind: 'email' }
        case 'uuid': return { ...base, ...strs, kind: 'uuid' }
        case 'date-time': return { ...base, kind: 'datetime' }
        case 'date': return { ...base, kind: 'date' }
        default: return { ...base, ...strs, kind: (num(p.maxLength) ?? 0) > 200 ? 'textarea' : 'text' }
      }
    }
    case 'integer':
    case 'number': {
      if (Array.isArray(p.enum)) return `"${key}" has a numeric choice list`
      const min = num(p.minimum) ?? (num(p.exclusiveMinimum) !== undefined ? num(p.exclusiveMinimum)! + (type === 'integer' ? 1 : Number.EPSILON) : undefined)
      const max = num(p.maximum) ?? (num(p.exclusiveMaximum) !== undefined ? num(p.exclusiveMaximum)! - (type === 'integer' ? 1 : Number.EPSILON) : undefined)
      return { ...base, kind: type, ...(min !== undefined ? { minimum: min } : {}), ...(max !== undefined ? { maximum: max } : {}) }
    }
    case 'boolean':
      return { ...base, kind: 'boolean' }
    case 'array': {
      const items = p.items
      if (!isObject(items) || typeOf(items) !== 'string' || Array.isArray(items.enum) || items.format) return `"${key}" is a list of something other than plain text`
      return {
        ...base,
        kind: 'string-list',
        ...(num(p.minItems) !== undefined ? { minItems: num(p.minItems)! } : {}),
        ...(num(p.maxItems) !== undefined ? { maxItems: num(p.maxItems)! } : {}),
      }
    }
    default:
      return `"${key}" has an unsupported type`
  }
}

/** Derives the form fields of a payload schema, or reports why it needs the JSON editor. */
export function schemaToForm(schema: JsonSchema | null | undefined): SchemaForm {
  if (!isObject(schema)) return { supported: false, reason: 'This task type accepts any JSON object.', fields: [] }
  for (const c of COMBINATORS) if (c in schema) return { supported: false, reason: `The schema uses ${c}.`, fields: [] }
  if (schema.type !== undefined && schema.type !== 'object') return { supported: false, reason: 'The payload is not an object.', fields: [] }
  const props = schema.properties
  if (props !== undefined && !isObject(props)) return { supported: false, reason: 'The schema properties are not readable.', fields: [] }
  const entries = Object.entries(props ?? {})
  if (!entries.length && schema.additionalProperties !== false) return { supported: false, reason: 'This task type accepts any JSON object.', fields: [] }
  if (isObject(schema.additionalProperties)) return { supported: false, reason: 'The schema allows extra typed properties.', fields: [] }
  const required = new Set(Array.isArray(schema.required) ? schema.required.filter((x): x is string => typeof x === 'string') : [])
  const fields: SchemaField[] = []
  for (const [key, p] of entries) {
    if (!isObject(p)) return { supported: false, reason: `"${key}" is not a schema.`, fields: [] }
    const f = fieldOf(key, p, required.has(key))
    if (typeof f === 'string') return { supported: false, reason: f + '.', fields: [] }
    fields.push(f)
  }
  return { supported: true, fields }
}

/** The payload with every missing property that has a schema default filled in. */
export function applyDefaults(schema: JsonSchema | null | undefined, value: Record<string, unknown> = {}): Record<string, unknown> {
  const out: Record<string, unknown> = { ...value }
  if (!isObject(schema) || !isObject(schema.properties)) return out
  for (const [key, p] of Object.entries(schema.properties)) {
    if (isObject(p) && 'default' in p && out[key] === undefined) out[key] = JSON.parse(JSON.stringify(p.default)) as unknown
  }
  return out
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

const isBlank = (v: unknown) => v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)

/**
 * Client-side check of a generated form's values (the server re-validates
 * against the full schema). Returns messages keyed by JSON pointer.
 */
export function validatePayload(fields: SchemaField[], value: Record<string, unknown>): Record<string, string> {
  const errors: Record<string, string> = {}
  for (const f of fields) {
    const v = value[f.key]
    if (isBlank(v)) {
      if (f.required && f.kind !== 'boolean') errors[f.pointer] = 'This field is required.'
      continue
    }
    const fail = (m: string) => (errors[f.pointer] = m)
    switch (f.kind) {
      case 'integer':
      case 'number':
        if (typeof v !== 'number' || !Number.isFinite(v)) fail('Enter a number.')
        else if (f.kind === 'integer' && !Number.isInteger(v)) fail('Enter a whole number.')
        else if (f.minimum !== undefined && v < f.minimum) fail(`Must be at least ${f.minimum}.`)
        else if (f.maximum !== undefined && v > f.maximum) fail(`Must be at most ${f.maximum}.`)
        break
      case 'select':
        if (!f.options?.includes(String(v))) fail('Choose one of the listed values.')
        break
      case 'string-list':
        if (!Array.isArray(v)) fail('Enter a list of values.')
        else if (f.minItems !== undefined && v.length < f.minItems) fail(`Add at least ${f.minItems}.`)
        else if (f.maxItems !== undefined && v.length > f.maxItems) fail(`Add at most ${f.maxItems}.`)
        break
      case 'boolean':
        break
      case 'datetime':
        if (typeof v !== 'string' || Number.isNaN(Date.parse(v))) fail('Enter a valid date and time.')
        break
      case 'date':
        if (typeof v !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(v)) fail('Enter a valid date.')
        break
      default: {
        const s = String(v)
        if (f.kind === 'email' && !EMAIL.test(s)) fail('Enter a valid e-mail address.')
        else if (f.kind === 'uuid' && !UUID.test(s)) fail('Enter a valid identifier (UUID).')
        else if (f.minLength !== undefined && s.length < f.minLength) fail(`Must be at least ${f.minLength} characters.`)
        else if (f.maxLength !== undefined && s.length > f.maxLength) fail(`Must be at most ${f.maxLength} characters.`)
        else if (f.pattern) {
          try {
            if (!new RegExp(f.pattern, 'u').test(s)) fail('This value has the wrong format.')
          } catch {
            // an unparsable pattern is left to the server
          }
        }
      }
    }
  }
  return errors
}

/** Drops blank optional values so the payload only carries what was entered. */
export function cleanPayload(fields: SchemaField[], value: Record<string, unknown>): Record<string, unknown> {
  const known = new Set(fields.map((f) => f.key))
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(value)) {
    if (known.has(k) && (v === '' || v === undefined || v === null)) continue
    if (v !== undefined) out[k] = v
  }
  return out
}

/** ISO date-time → the value of an <input type="datetime-local"> (local time). */
export function toLocalInput(iso: unknown): string {
  if (typeof iso !== 'string' || !iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

/** An <input type="datetime-local"> value → ISO 8601 UTC ('' when blank or invalid). */
export function fromLocalInput(local: unknown): string {
  if (typeof local !== 'string' || !local) return ''
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}
