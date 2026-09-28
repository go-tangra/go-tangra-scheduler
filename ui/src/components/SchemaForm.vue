<script setup lang="ts">
// Payload editor generated from a task type's JSON Schema (utils/jsonSchema):
// one field per property with required markers, descriptions as help text,
// min/max, choice lists and formats; array-of-strings as chips. A "JSON"
// toggle switches to a raw JSON editor, which is the only editor when the
// schema has constructs the form cannot express (or is null). Server errors
// arrive keyed by JSON pointer (`/recipient`) and show under their field.
import { computed, ref, watch } from 'vue'
import { UiAlert, UiButton, UiInput, UiSelect, UiSwitch, UiTextarea } from '@go-tangra/ui'
import ChipInput from './ChipInput.vue'
import type { JsonSchema } from '@/api/types'
import { fromLocalInput, pointerToKey, schemaToForm, toLocalInput, validatePayload, type SchemaField } from '@/utils/jsonSchema'

const props = withDefaults(defineProps<{
  schema?: JsonSchema | null | undefined
  modelValue: Record<string, unknown>
  /** Server errors keyed by JSON pointer ('' = the payload as a whole). */
  errors?: Record<string, string> | undefined
  idPrefix?: string | undefined
  disabled?: boolean | undefined
}>(), { schema: null, errors: () => ({}), idPrefix: 'payload', disabled: false })
const emit = defineEmits<{ (e: 'update:modelValue', v: Record<string, unknown>): void }>()

const form = computed(() => schemaToForm(props.schema))
const jsonMode = ref(false)
const jsonText = ref('')
const jsonError = ref('')
const localErrors = ref<Record<string, string>>({})

const pretty = (v: unknown) => JSON.stringify(v ?? {}, null, 2)

watch(form, (f) => {
  jsonMode.value = !f.supported
  jsonText.value = pretty(props.modelValue)
  jsonError.value = ''
  localErrors.value = {}
}, { immediate: true })

// Keep the JSON text in step with outside changes (type defaults, reset).
watch(() => props.modelValue, (v) => {
  if (!jsonMode.value) return
  try {
    if (JSON.stringify(JSON.parse(jsonText.value)) === JSON.stringify(v)) return
  } catch {
    return // the user is mid-edit; keep their text
  }
  jsonText.value = pretty(v)
})

const idOf = (f: SchemaField) => `${props.idPrefix}-${f.key}`
const valueOf = (f: SchemaField) => props.modelValue[f.key]

function set(key: string, v: unknown): void {
  const next = { ...props.modelValue }
  if (v === undefined) delete next[key]
  else next[key] = v
  localErrors.value = Object.fromEntries(Object.entries(localErrors.value).filter(([p]) => pointerToKey(p) !== key))
  emit('update:modelValue', next)
}

/** Server errors by property name (nested pointers land on their top-level field). */
const serverByKey = computed(() => {
  const out: Record<string, string> = {}
  for (const [p, m] of Object.entries(props.errors ?? {})) {
    const k = pointerToKey(p)
    if (k && !(k in out)) out[k] = m
  }
  return out
})
const keys = computed(() => new Set(form.value.fields.map((f) => f.key)))
/** Errors that have no field to sit under (the whole payload, unknown properties, JSON mode). */
const generalErrors = computed(() =>
  Object.entries(props.errors ?? {})
    .filter(([p]) => jsonMode.value || !keys.value.has(pointerToKey(p)))
    .map(([p, m]) => (p && p !== '/' ? `${p}: ${m}` : m)),
)
const errorOf = (f: SchemaField) => localErrors.value[f.pointer] ?? serverByKey.value[f.key]

function onJson(text: unknown): void {
  jsonText.value = String(text ?? '')
  const t = jsonText.value.trim()
  if (!t) {
    jsonError.value = ''
    emit('update:modelValue', {})
    return
  }
  try {
    const v = JSON.parse(t) as unknown
    if (typeof v !== 'object' || v === null || Array.isArray(v)) {
      jsonError.value = 'The payload must be a JSON object ({ … }).'
      return
    }
    jsonError.value = ''
    emit('update:modelValue', v as Record<string, unknown>)
  } catch (e) {
    jsonError.value = 'Not valid JSON: ' + (e instanceof Error ? e.message : String(e))
  }
}

function toggle(): void {
  if (!jsonMode.value) {
    jsonText.value = pretty(props.modelValue)
    jsonError.value = ''
    jsonMode.value = true
    return
  }
  if (jsonError.value) return // fix the JSON first
  jsonMode.value = false
}

/** Checks the payload before saving; false keeps the drawer open with errors shown. */
function validate(): boolean {
  if (jsonMode.value) {
    onJson(jsonText.value)
    return !jsonError.value
  }
  localErrors.value = validatePayload(form.value.fields, props.modelValue)
  return Object.keys(localErrors.value).length === 0
}
defineExpose({ validate, jsonMode })

const inputType = (f: SchemaField) => (f.kind === 'email' ? 'email' : f.kind === 'integer' || f.kind === 'number' ? 'number' : f.kind === 'date' ? 'date' : f.kind === 'datetime' ? 'datetime-local' : 'text')
const numberValue = (v: unknown) => (typeof v === 'number' ? v : '')
function setNumber(f: SchemaField, v: unknown): void {
  set(f.key, v === '' || v === null || v === undefined ? undefined : Number(v))
}
function setText(f: SchemaField, v: unknown): void {
  const s = String(v ?? '')
  set(f.key, s === '' ? undefined : f.kind === 'datetime' ? fromLocalInput(s) || s : s)
}
const textValue = (f: SchemaField) => (f.kind === 'datetime' ? toLocalInput(valueOf(f)) : ((valueOf(f) as string | undefined) ?? ''))
const options = (f: SchemaField) => (f.options ?? []).map((o) => ({ title: o, value: o }))
const hintOf = (f: SchemaField) => {
  const parts = [f.description]
  if (f.kind === 'uuid') parts.push('A UUID, e.g. 018f3a2b-0000-7000-8000-000000000001.')
  if ((f.kind === 'integer' || f.kind === 'number') && (f.minimum !== undefined || f.maximum !== undefined)) {
    parts.push(f.minimum !== undefined && f.maximum !== undefined ? `Between ${f.minimum} and ${f.maximum}.` : f.minimum !== undefined ? `At least ${f.minimum}.` : `At most ${f.maximum}.`)
  }
  const h = parts.filter(Boolean).join(' ')
  return h || undefined
}
</script>

<template>
  <div class="flex flex-col gap-3" data-test="schema-form">
    <div class="flex flex-wrap items-center gap-2">
      <p v-if="!form.supported" class="grow text-sm text-base-content/70" data-test="schema-form-fallback">
        {{ form.reason }} Enter the payload as JSON.
      </p>
      <span v-else class="grow" />
      <UiButton v-if="form.supported" size="xs" variant="text" :icon="jsonMode ? 'mdi-form-select' : 'mdi-code-json'" :aria-pressed="jsonMode" :disabled="jsonMode && !!jsonError" data-test="schema-form-json-toggle" @click="toggle">
        {{ jsonMode ? 'Use form' : 'JSON' }}
      </UiButton>
    </div>

    <UiAlert v-if="generalErrors.length" kind="error" data-test="schema-form-errors">
      <ul class="list-inside list-disc">
        <li v-for="m in generalErrors" :key="m">{{ m }}</li>
      </ul>
    </UiAlert>

    <UiTextarea
      v-if="jsonMode"
      :id="idPrefix + '-json'"
      :model-value="jsonText"
      label="Payload (JSON)"
      :rows="10"
      :error="jsonError || undefined"
      :disabled="disabled"
      class="font-mono"
      data-test="schema-form-json"
      @update:model-value="onJson"
    />

    <template v-else>
      <p v-if="!form.fields.length" class="text-sm text-base-content/70" data-test="schema-form-empty">This task type takes no payload.</p>
      <div v-else class="grid grid-cols-1 gap-3 md:grid-cols-2">
        <template v-for="f in form.fields" :key="f.key">
          <div v-if="f.kind === 'boolean'" class="flex items-end" :data-test="'schema-field-' + f.key">
            <UiSwitch :id="idOf(f)" :model-value="valueOf(f) === true" :label="f.label" :hint="hintOf(f)" :disabled="disabled" @update:model-value="set(f.key, $event)" />
          </div>
          <div v-else-if="f.kind === 'select'" :data-test="'schema-field-' + f.key">
            <UiSelect :id="idOf(f)" :model-value="valueOf(f) ?? ''" :label="f.label" :options="options(f)" :hint="hintOf(f)" :error="errorOf(f)" :required="f.required" :clearable="!f.required" :disabled="disabled" @update:model-value="set(f.key, $event === '' || $event === null ? undefined : $event)" />
          </div>
          <div v-else-if="f.kind === 'string-list'" class="md:col-span-2" :data-test="'schema-field-' + f.key">
            <ChipInput :id="idOf(f)" :model-value="valueOf(f)" :label="f.label" :hint="hintOf(f)" :error="errorOf(f)" :required="f.required" :max-items="f.maxItems" :disabled="disabled" @update:model-value="set(f.key, $event.length ? $event : undefined)" />
          </div>
          <div v-else-if="f.kind === 'textarea'" class="md:col-span-2" :data-test="'schema-field-' + f.key">
            <UiTextarea :id="idOf(f)" :model-value="textValue(f)" :label="f.label" :hint="hintOf(f)" :error="errorOf(f)" :required="f.required" :disabled="disabled" @update:model-value="setText(f, $event)" />
          </div>
          <div v-else-if="f.kind === 'integer' || f.kind === 'number'" :data-test="'schema-field-' + f.key">
            <UiInput :id="idOf(f)" type="number" :model-value="numberValue(valueOf(f))" :label="f.label" :hint="hintOf(f)" :error="errorOf(f)" :required="f.required" :min="f.minimum" :max="f.maximum" :step="f.kind === 'integer' ? 1 : 'any'" :disabled="disabled" @update:model-value="setNumber(f, $event)" />
          </div>
          <div v-else :data-test="'schema-field-' + f.key">
            <UiInput :id="idOf(f)" :type="inputType(f)" :model-value="textValue(f)" :label="f.label" :hint="hintOf(f)" :error="errorOf(f)" :required="f.required" :disabled="disabled" :inputmode="f.kind === 'email' ? 'email' : undefined" @update:model-value="setText(f, $event)" />
          </div>
        </template>
      </div>
    </template>
  </div>
</template>
