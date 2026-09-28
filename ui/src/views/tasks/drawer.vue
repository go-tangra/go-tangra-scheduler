<script setup lang="ts">
// Task drawer: create or edit a task, and (for an existing task) its
// execution history. Picking a type pre-fills the cron expression and max
// retries from the type's defaults and the payload from its schema defaults.
// Periodic tasks get a cron field, a time zone and a live preview of the next
// five runs (GET /cron/preview, debounced); one-shot tasks run now, after a
// delay or at a time. The payload form is generated from the type's JSON
// Schema (components/SchemaForm.vue) with a raw JSON fallback. Server 422
// errors land on the field named by detail.field (a payload JSON pointer on
// its schema field). Saving a wait-for-result task opens its follow view.
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useAbility } from '@casl/vue'
import { UiAlert, UiButton, UiDrawer, UiInput, UiKeyValueTable, UiSection, UiSelect, UiSwitch, UiTabs, UiTextarea, type KeyValue, type SelectOption, type TabItem } from '@go-tangra/ui'
import SchemaForm from '@/components/SchemaForm.vue'
import History from './history.vue'
import type { Kind, Task } from '@/api/types'
import { useTasks } from '@/stores/tasks'
import { useTypes } from '@/stores/types'
import { DELAY_UNITS, KINDS, KIND_LABELS, STATE_LABELS, VALIDITY_LABELS, taskErrorTarget, taskFormSchema, toCreateBody, toUpdateBody, type DelayUnit, type WhenMode } from '@/schemas'
import { describeCron, timeZones } from '@/utils/cron'
import { applyDefaults, cleanPayload, schemaToForm, toLocalInput } from '@/utils/jsonSchema'
import { when as fmtWhen } from '@/utils/format'

const props = withDefaults(defineProps<{
  open: boolean
  /** null creates a new task. */
  task: Task | null
  initialTab?: 'settings' | 'history' | undefined
}>(), { initialTab: 'settings' })
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', t: Task): void
  (e: 'follow', executionId: string): void
  (e: 'open-execution', executionId: string): void
}>()

const tasks = useTasks()
const types = useTypes()
const ability = useAbility()
const canEdit = computed(() => (props.task ? ability.can('update', 'SchedulerTask') : ability.can('create', 'SchedulerTask')))
const canHistory = computed(() => ability.can('read', 'SchedulerExecution'))

interface Draft {
  name: string
  type_name: string
  kind: Kind
  cron: string
  timezone: string
  when: WhenMode
  delay_amount: number | ''
  delay_unit: DelayUnit
  run_at: string
  max_retries: number | ''
  timeout_seconds: number | ''
  catch_up: boolean
  enabled: boolean
  remark: string
  payload: Record<string, unknown>
}
const blank = (): Draft => ({
  name: '', type_name: '', kind: 'periodic', cron: '', timezone: 'UTC', when: 'now', delay_amount: '', delay_unit: 'minutes', run_at: '',
  max_retries: '', timeout_seconds: '', catch_up: false, enabled: true, remark: '', payload: {},
})
const draft = reactive<Draft>(blank())
const fieldErrors = ref<Record<string, string>>({})
const payloadErrors = ref<Record<string, string>>({})
const banner = ref('')
const saving = ref(false)
const tab = ref<'settings' | 'history'>('settings')
const schemaForm = ref<InstanceType<typeof SchemaForm> | null>(null)

function reset(): void {
  Object.assign(draft, blank())
  fieldErrors.value = {}
  payloadErrors.value = {}
  banner.value = ''
  const t = props.task
  tab.value = t && props.initialTab === 'history' && canHistory.value ? 'history' : 'settings'
  if (!t) return
  Object.assign(draft, {
    name: t.name,
    type_name: t.type_name,
    kind: t.kind,
    cron: t.cron ?? '',
    timezone: t.timezone || 'UTC',
    when: 'at' as WhenMode,
    run_at: toLocalInput(t.run_at),
    max_retries: t.max_retries ?? '',
    timeout_seconds: t.timeout_seconds ?? '',
    catch_up: t.catch_up ?? false,
    enabled: t.enabled,
    remark: t.remark ?? '',
    payload: JSON.parse(JSON.stringify(t.payload ?? {})) as Record<string, unknown>,
  })
}
watch(() => [props.open, props.task?.id, props.initialTab], () => {
  if (!props.open) return
  reset()
  void types.list()
}, { immediate: true })

// --- type picker ---
const selectedType = computed(() => types.find(draft.type_name))
const typeOptions = computed<SelectOption[]>(() =>
  types.items
    .filter((t) => t.available || t.name === draft.type_name)
    .map((t) => ({ title: `${t.display_name} (${t.module}${t.scope === 'platform' ? ', platform' : ''})`, value: t.name })),
)
function pickType(v: unknown): void {
  const name = typeof v === 'string' ? v : ''
  draft.type_name = name
  const t = types.find(name)
  if (!t) return
  if (t.default_cron) draft.cron = t.default_cron
  if (t.default_max_retries !== undefined) draft.max_retries = t.default_max_retries
  if (!draft.name) draft.name = t.display_name
  draft.payload = applyDefaults(t.payload_schema ?? null, {})
  payloadErrors.value = {}
}
const payloadSchema = computed(() => selectedType.value?.payload_schema ?? null)

// --- kind / schedule ---
const kindOptions: SelectOption[] = KINDS.map((k) => ({ title: KIND_LABELS[k], value: k }))
const zoneOptions: SelectOption[] = timeZones().map((z) => ({ title: z, value: z }))
const whenOptions = computed<SelectOption[]>(() => [
  ...(props.task ? [] : [{ title: 'Run now', value: 'now' }]),
  { title: 'After a delay', value: 'delay' },
  { title: 'At a date and time', value: 'at' },
])
const unitOptions: SelectOption[] = DELAY_UNITS.map((u) => ({ title: u === 'hours' ? 'Hours' : 'Minutes', value: u }))
const cronWords = computed(() => {
  const w = describeCron(draft.cron)
  return w && w !== draft.cron.trim() ? w : ''
})

// Live preview of the next five runs (server-side parser), debounced.
const preview = ref<string[]>([])
const previewError = ref('')
const previewing = ref(false)
let previewTimer: ReturnType<typeof setTimeout> | null = null
let previewAbort: AbortController | null = null
function cancelPreview(): void {
  if (previewTimer) clearTimeout(previewTimer)
  previewTimer = null
  previewAbort?.abort()
  previewAbort = null
}
watch(() => [props.open, draft.kind, draft.cron, draft.timezone], () => {
  cancelPreview()
  preview.value = []
  previewError.value = ''
  const expr = draft.cron.trim()
  if (!props.open || draft.kind !== 'periodic' || !expr || !canEdit.value) return
  previewTimer = setTimeout(async () => {
    const ctl = new AbortController()
    previewAbort = ctl
    previewing.value = true
    try {
      preview.value = await tasks.previewCron(expr, draft.timezone || 'UTC', 5, ctl.signal)
    } catch (e) {
      if (!ctl.signal.aborted) previewError.value = taskErrorTarget(e).message
    } finally {
      if (previewAbort === ctl) previewing.value = false
    }
  }, 400)
}, { immediate: true })
onBeforeUnmount(cancelPreview)
function inZone(iso: string): string {
  try {
    return new Date(iso).toLocaleString(undefined, { timeZone: draft.timezone || 'UTC', dateStyle: 'medium', timeStyle: 'short' })
  } catch {
    return fmtWhen(iso)
  }
}

// --- save ---
async function save(): Promise<void> {
  banner.value = ''
  payloadErrors.value = {}
  const parsed = taskFormSchema.safeParse({ ...draft })
  const errs: Record<string, string> = {}
  if (!parsed.success) for (const i of parsed.error.issues) errs[String(i.path[0] ?? '')] ??= i.message
  fieldErrors.value = errs
  const payloadOk = schemaForm.value?.validate() ?? true
  if (!parsed.success || !payloadOk) {
    banner.value = 'Please check the highlighted fields.'
    return
  }
  const form = schemaToForm(payloadSchema.value)
  const payload = form.supported && !schemaForm.value?.jsonMode ? cleanPayload(form.fields, draft.payload) : draft.payload
  saving.value = true
  try {
    if (props.task) {
      const t = await tasks.update(props.task.id, toUpdateBody(parsed.data, payload))
      emit('saved', t)
    } else {
      const t = await tasks.create(toCreateBody(parsed.data, payload))
      emit('saved', t)
      if (t.kind === 'wait_result' && t.execution_id) emit('follow', t.execution_id)
    }
    emit('close')
  } catch (e) {
    const target = taskErrorTarget(e)
    if (target.pointer !== undefined) payloadErrors.value = { [target.pointer]: target.message }
    else if (target.field) fieldErrors.value = { [target.field]: target.message }
    else banner.value = target.message
  } finally {
    saving.value = false
  }
}

// --- read-only view (no update permission) ---
const readOnly = computed(() => !!props.task && !canEdit.value)
const details = computed<KeyValue[]>(() => {
  const t = props.task
  if (!t) return []
  return [
    { label: 'Type', value: `${t.type_display_name || t.type_name} (${t.module})` },
    { label: 'Kind', value: KIND_LABELS[t.kind] },
    { label: 'State', value: STATE_LABELS[t.state] },
    { label: 'Validity', value: VALIDITY_LABELS[t.validity] + (t.validity_message ? ` — ${t.validity_message}` : '') },
    ...(t.kind === 'periodic' ? [{ label: 'Schedule', value: `${describeCron(t.cron)} (${t.timezone || 'UTC'})` }] : [{ label: 'Runs at', value: fmtWhen(t.run_at) }]),
    { label: 'Next run', value: fmtWhen(t.next_run_at) },
    { label: 'Max retries', value: t.max_retries },
    { label: 'Timeout', value: t.timeout_seconds ? `${t.timeout_seconds} s` : '' },
    { label: 'Remark', value: t.remark },
    { label: 'Task id', value: t.id, copyable: true },
  ]
})

const tabs = computed<TabItem[]>(() => [
  { key: 'settings', label: readOnly.value ? 'Details' : 'Settings', icon: 'mdi-cog-outline' },
  ...(canHistory.value ? [{ key: 'history', label: 'History', icon: 'mdi-history' }] : []),
])
const title = computed(() => (props.task ? props.task.name : 'New task'))
</script>

<template>
  <UiDrawer :model-value="open" :title="title" size="xl" data-test="task-drawer" @update:model-value="emit('close')">
    <UiTabs v-if="task" v-model="tab" :tabs="tabs" class="mb-4" />

    <History v-if="task && tab === 'history'" :task-id="task.id" @open="emit('open-execution', $event)" />

    <section v-else-if="readOnly && task" class="flex flex-col gap-4" data-test="task-details">
      <UiKeyValueTable :items="details" :columns="2" />
      <div>
        <h3 class="mb-1 text-sm font-semibold">Payload</h3>
        <pre class="overflow-auto rounded-box bg-base-200 p-3 text-xs">{{ JSON.stringify(task.payload ?? {}, null, 2) }}</pre>
      </div>
    </section>

    <div v-else class="flex flex-col gap-6" data-test="task-form">
      <UiAlert v-if="banner" kind="error" data-test="task-form-error">{{ banner }}</UiAlert>
      <UiAlert v-if="task && task.validity !== 'ok'" kind="warning" data-test="task-validity">
        {{ VALIDITY_LABELS[task.validity] }}{{ task.validity_message ? ': ' + task.validity_message : '' }}
      </UiAlert>

      <UiSection title="Task">
        <div class="grid grid-cols-1 gap-3 md:grid-cols-12">
          <div class="md:col-span-6">
            <UiSelect v-if="!task" id="task-type" :model-value="draft.type_name" label="Task type" :options="typeOptions" :error="fieldErrors.type_name" required :clearable="false" placeholder="Choose a type" data-test="task-type" @update:model-value="pickType" />
            <UiInput v-else id="task-type" :model-value="`${task.type_display_name || task.type_name} (${task.module})`" label="Task type" readonly hint="Fixed after creation." />
          </div>
          <div class="md:col-span-6"><UiInput id="task-name" v-model="draft.name" label="Name" required :error="fieldErrors.name" data-test="task-name" /></div>
          <p v-if="selectedType" class="text-sm text-base-content/70 md:col-span-12" data-test="task-type-info">
            <span class="font-medium">{{ selectedType.display_name }}</span> — module {{ selectedType.module }}<template v-if="selectedType.description">. {{ selectedType.description }}</template>
            <template v-if="!selectedType.available"> (currently unavailable)</template>
          </p>
          <div class="md:col-span-6">
            <UiSelect v-if="!task" id="task-kind" v-model="draft.kind" label="Kind" :options="kindOptions" :clearable="false" data-test="task-kind" />
            <UiInput v-else id="task-kind" :model-value="KIND_LABELS[task.kind]" label="Kind" readonly hint="Fixed after creation." />
          </div>
        </div>
      </UiSection>

      <UiSection v-if="draft.kind === 'periodic'" title="Schedule" description="Five-field cron: minute hour day-of-month month day-of-week.">
        <div class="grid grid-cols-1 gap-3 md:grid-cols-12">
          <div class="md:col-span-7"><UiInput id="task-cron" v-model="draft.cron" label="Cron expression" required placeholder="0 3 * * *" :hint="cronWords || undefined" :error="fieldErrors.cron" class="font-mono" data-test="task-cron" /></div>
          <div class="md:col-span-5"><UiSelect id="task-timezone" v-model="draft.timezone" label="Time zone" :options="zoneOptions" :clearable="false" :error="fieldErrors.timezone" data-test="task-timezone" /></div>
        </div>
        <div class="rounded-box border border-base-300 p-3 text-sm" aria-live="polite" data-test="cron-preview">
          <p class="mb-1 font-medium">Next runs</p>
          <p v-if="previewError" class="text-error" data-test="cron-preview-error">{{ previewError }}</p>
          <p v-else-if="!draft.cron.trim()" class="text-base-content/70">Enter a cron expression to see the next runs.</p>
          <p v-else-if="previewing && !preview.length" class="text-base-content/70">Calculating…</p>
          <ol v-else class="list-inside list-decimal">
            <li v-for="n in preview" :key="n">{{ inZone(n) }} <span class="text-base-content/70">({{ draft.timezone || 'UTC' }})</span></li>
          </ol>
        </div>
      </UiSection>

      <UiSection v-else title="When" :description="draft.kind === 'wait_result' ? 'The run is followed live after saving.' : undefined">
        <div class="grid grid-cols-1 gap-3 md:grid-cols-12">
          <div class="md:col-span-4"><UiSelect id="task-when" v-model="draft.when" label="Run" :options="whenOptions" :clearable="false" data-test="task-when" /></div>
          <template v-if="draft.when === 'delay'">
            <div class="md:col-span-4"><UiInput id="task-delay" v-model="draft.delay_amount" type="number" :min="1" label="Delay" required :error="fieldErrors.delay_amount" data-test="task-delay" /></div>
            <div class="md:col-span-4"><UiSelect id="task-delay-unit" v-model="draft.delay_unit" label="Unit" :options="unitOptions" :clearable="false" data-test="task-delay-unit" /></div>
          </template>
          <div v-else-if="draft.when === 'at'" class="md:col-span-8"><UiInput id="task-run-at" v-model="draft.run_at" type="datetime-local" label="Run at (your local time)" required :error="fieldErrors.run_at" data-test="task-run-at" /></div>
        </div>
      </UiSection>

      <UiSection title="Payload" :description="selectedType ? undefined : 'Choose a task type first.'">
        <SchemaForm v-if="draft.type_name" ref="schemaForm" v-model="draft.payload" :schema="payloadSchema" :errors="payloadErrors" id-prefix="payload" />
      </UiSection>

      <UiSection title="Options">
        <div class="grid grid-cols-1 gap-3 md:grid-cols-12">
          <div class="md:col-span-4"><UiInput id="task-retries" v-model="draft.max_retries" type="number" :min="0" :max="10" label="Max retries" hint="0 to 10." :error="fieldErrors.max_retries" data-test="task-retries" /></div>
          <div class="md:col-span-4"><UiInput id="task-timeout" v-model="draft.timeout_seconds" type="number" :min="1" :max="86400" label="Timeout (seconds)" hint="Empty uses the module default." :error="fieldErrors.timeout_seconds" data-test="task-timeout" /></div>
          <div class="flex flex-col justify-end gap-2 md:col-span-4">
            <UiSwitch id="task-enabled" v-model="draft.enabled" label="Enabled" />
            <UiSwitch v-if="draft.kind === 'periodic'" id="task-catch-up" v-model="draft.catch_up" label="Catch up missed runs" />
          </div>
          <div class="md:col-span-12"><UiTextarea id="task-remark" v-model="draft.remark" label="Remark" :rows="2" /></div>
        </div>
      </UiSection>
      <p v-if="task" class="text-xs text-base-content/70">State: {{ STATE_LABELS[task.state] }} · next run {{ fmtWhen(task.next_run_at) || '—' }}</p>
    </div>

    <template v-if="canEdit && tab === 'settings'" #actions>
      <UiButton variant="text" @click="emit('close')">Cancel</UiButton>
      <UiButton icon="mdi-check" :loading="saving" data-test="task-save" @click="save">{{ task ? 'Save' : 'Create' }}</UiButton>
    </template>
  </UiDrawer>
</template>
