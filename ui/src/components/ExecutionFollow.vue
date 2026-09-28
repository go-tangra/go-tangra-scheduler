<script setup lang="ts">
// Follows one execution live (research D7): subscribes to the scheduler SSE
// stream and re-reads GET /executions/{id} on a matching `scheduler.execution`
// event, and also polls every 3 s while the attempt is queued or running
// (a platform administrator following another tenant's run gets no events).
// When an attempt fails and retries remain, it keeps watching the task for
// the next attempt of the same occurrence and follows that one. Shows
// queued → running → final, the attempt, times, message and result.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { UiAlert, UiBadge, UiKeyValueTable, UiLiveIndicator, UiStatusChip, type KeyValue } from '@go-tangra/ui'
import { describe } from '@/api/client'
import type { Execution } from '@/api/types'
import { useExecutions } from '@/stores/executions'
import { POLL_MS, useLive } from '@/stores/live'
import { EXECUTION_STATUS_COLORS, EXECUTION_STATUS_LABELS, TRIGGER_LABELS, isFinal } from '@/schemas'
import { duration, resultText, when } from '@/utils/format'

const props = withDefaults(defineProps<{
  executionId: string
  /** Switch to the next attempt of the occurrence when this one fails with retries left. */
  followRetries?: boolean | undefined
}>(), { followRetries: true })
const emit = defineEmits<{ (e: 'final', v: Execution): void; (e: 'update', v: Execution): void }>()

const store = useExecutions()
const live = useLive()
const current = ref<Execution | null>(null)
const previous = ref<Execution[]>([])
const error = ref('')
const loading = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null
let disposed = false
let finalEmitted = ''

const final = computed(() => isFinal(current.value?.status))
const retryPending = computed(() => {
  const e = current.value
  return !!e && props.followRetries && (e.status === 'failed' || e.status === 'timed_out') && e.attempt < e.max_attempts
})

function stop(): void {
  if (timer) clearTimeout(timer)
  timer = null
}

function schedule(): void {
  stop()
  if (disposed || !current.value) return
  if (!final.value) timer = setTimeout(() => void refresh(), POLL_MS)
  else if (retryPending.value) timer = setTimeout(() => void findRetry(), POLL_MS)
  else if (finalEmitted !== current.value.id) {
    finalEmitted = current.value.id
    emit('final', current.value)
  }
}

async function refresh(id = current.value?.id ?? props.executionId): Promise<void> {
  if (disposed) return
  loading.value = true
  try {
    const e = await store.get(id)
    if (disposed) return
    current.value = e
    error.value = ''
    emit('update', e)
  } catch (e) {
    error.value = describe(e)
  } finally {
    loading.value = false
  }
  schedule()
}

/** Looks for a later attempt of the same occurrence and follows it. */
async function findRetry(): Promise<void> {
  const cur = current.value
  if (disposed || !cur) return
  try {
    const page = await store.fetchPage({ task_id: cur.task_id }, 1, 10)
    const next = (page.items ?? [])
      .filter((x) => x.occurrence_id === cur.occurrence_id && x.attempt > cur.attempt)
      .sort((a, b) => a.attempt - b.attempt)[0]
    if (next && !disposed) {
      previous.value = [...previous.value, cur]
      await refresh(next.id)
      return
    }
  } catch {
    // keep waiting; the stream or the next poll will pick the retry up
  }
  schedule()
}

const off = live.on((ev) => {
  if (ev.type !== 'scheduler.execution' || !current.value) return
  if (ev.data.execution_id === current.value.id) void refresh()
  else if (ev.data.task_id === current.value.task_id && retryPending.value) void findRetry()
})
const release = live.connect()

watch(() => props.executionId, (id) => {
  stop()
  current.value = null
  previous.value = []
  finalEmitted = ''
  void refresh(id)
}, { immediate: true })

onBeforeUnmount(() => {
  disposed = true
  stop()
  off()
  release()
})

// --- rendering ---
const STEPS = ['queued', 'running', 'done'] as const
const stepIndex = computed(() => {
  const s = current.value?.status
  return s === 'queued' ? 0 : s === 'running' ? 1 : 2
})
const finalLabel = computed(() => (current.value && final.value ? EXECUTION_STATUS_LABELS[current.value.status] : 'Finished'))
const details = computed<KeyValue[]>(() => {
  const e = current.value
  if (!e) return []
  const trigger = TRIGGER_LABELS[e.trigger] ?? e.trigger
  return [
    { label: 'Task', value: e.task_name || e.task_id },
    { label: 'Type', value: `${e.type_name} (${e.module})` },
    { label: 'Attempt', value: `${e.attempt} of ${e.max_attempts}` },
    { label: 'Trigger', value: e.trigger === 'manual' && e.triggered_by ? `${trigger} by ${e.triggered_by}` : trigger },
    { label: 'Scheduled', value: when(e.scheduled_at ?? e.due_at) },
    { label: 'Started', value: when(e.started_at) },
    { label: 'Finished', value: when(e.finished_at) },
    { label: 'Duration', value: duration(e.duration_ms) },
    { label: 'Execution id', value: e.id, copyable: true },
  ]
})
const result = computed(() => resultText(current.value?.result))
defineExpose({ current, refresh })
</script>

<template>
  <section class="flex flex-col gap-4" aria-live="polite" data-test="execution-follow">
    <UiAlert v-if="error" kind="error" data-test="execution-error">{{ error }}</UiAlert>
    <p v-if="!current && loading" class="text-sm text-base-content/70">Loading…</p>

    <template v-if="current">
      <div class="flex flex-wrap items-center gap-2">
        <UiStatusChip :status="current.status" :label="EXECUTION_STATUS_LABELS[current.status]" :colors="EXECUTION_STATUS_COLORS" data-test="execution-status" />
        <UiBadge color="neutral" size="sm" data-test="execution-attempt">Attempt {{ current.attempt }} of {{ current.max_attempts }}</UiBadge>
        <UiLiveIndicator v-if="!final || retryPending" :connected="live.connected" />
      </div>

      <ol class="steps w-full" aria-label="Progress">
        <li v-for="(s, i) in STEPS" :key="s" class="step" :class="i <= stepIndex ? (i === 2 && current.status !== 'succeeded' ? 'step-error' : 'step-primary') : ''" :aria-current="i === stepIndex ? 'step' : undefined">
          {{ s === 'queued' ? 'Queued' : s === 'running' ? 'Running' : finalLabel }}
        </li>
      </ol>

      <UiAlert v-if="retryPending" kind="warning" data-test="execution-retry-pending">
        Attempt {{ current.attempt }} of {{ current.max_attempts }} did not succeed; waiting for the retry.
      </UiAlert>

      <div>
        <h3 class="mb-1 text-sm font-semibold">Message</h3>
        <p v-if="current.message" class="text-sm whitespace-pre-wrap break-words" data-test="execution-message">{{ current.message }}</p>
        <p v-else class="text-sm text-base-content/70">No message.</p>
      </div>

      <div>
        <h3 class="mb-1 text-sm font-semibold">Result</h3>
        <pre v-if="result" class="max-h-96 overflow-auto rounded-box bg-base-200 p-3 text-xs whitespace-pre-wrap break-all" data-test="execution-result">{{ result }}</pre>
        <p v-else class="text-sm text-base-content/70">{{ final ? 'No result.' : 'The result appears when the run finishes.' }}</p>
        <p v-if="current.result_truncated" class="mt-1 text-xs text-warning" data-test="execution-truncated">The result was too large and has been truncated.</p>
      </div>

      <UiKeyValueTable :items="details" :columns="2" />

      <div v-if="previous.length" data-test="execution-previous">
        <h3 class="mb-1 text-sm font-semibold">Earlier attempts</h3>
        <ul class="divide-y divide-base-300 rounded-box border border-base-300 text-sm">
          <li v-for="p in previous" :key="p.id" class="flex flex-wrap items-center gap-2 px-3 py-2">
            <UiStatusChip :status="p.status" :label="EXECUTION_STATUS_LABELS[p.status]" :colors="EXECUTION_STATUS_COLORS" />
            <span>Attempt {{ p.attempt }}</span>
            <span class="grow break-words text-base-content/70">{{ p.message }}</span>
          </li>
        </ul>
      </div>
    </template>
  </section>
</template>
