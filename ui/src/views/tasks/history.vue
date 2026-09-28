<script setup lang="ts">
// A task's execution history (the task drawer's "History" tab): succeeded /
// failed / other counts, a failed-only toggle, a status filter, a time range
// and server paging. Selecting a row opens the execution drawer.
import { computed, onMounted, onUnmounted, reactive, watch } from 'vue'
import { UiAlert, UiBadge, UiButton, UiDataTable, UiInput, UiPagination, UiSelect, UiStatusChip, UiSwitch, type Column, type SelectOption } from '@go-tangra/ui'
import type { Execution, ExecutionFilter, ExecutionStatus } from '@/api/types'
import { useExecutions } from '@/stores/executions'
import { coalesce, useLive } from '@/stores/live'
import { EXECUTION_STATUSES, EXECUTION_STATUS_COLORS, EXECUTION_STATUS_LABELS, TRIGGER_LABELS } from '@/schemas'
import { duration, when } from '@/utils/format'
import { fromLocalInput } from '@/utils/jsonSchema'

const props = defineProps<{ taskId: string }>()
const emit = defineEmits<{ (e: 'open', executionId: string): void }>()

const store = useExecutions()
const live = useLive()
const f = reactive({ failedOnly: false, status: '' as ExecutionStatus | '', from: '', to: '' })

function filter(): ExecutionFilter {
  return {
    task_id: props.taskId,
    failed_only: f.failedOnly || undefined,
    status: f.status ? [f.status] : undefined,
    from: fromLocalInput(f.from) || undefined,
    to: fromLocalInput(f.to) || undefined,
  }
}
const load = (p = 1) => void store.list(filter(), p)
watch(() => props.taskId, () => load(), { immediate: false })
watch(() => [f.failedOnly, f.status], () => load())

// New attempts of this task appear live.
const refresh = coalesce(() => void store.reload())
let off: (() => void) | null = null
let release: (() => void) | null = null
onMounted(() => {
  load()
  release = live.connect()
  off = live.on((ev) => {
    if (ev.data.task_id === props.taskId) refresh.trigger()
  })
})
onUnmounted(() => {
  off?.()
  release?.()
  refresh.cancel()
})

const statusOptions: SelectOption[] = EXECUTION_STATUSES.map((s) => ({ title: EXECUTION_STATUS_LABELS[s], value: s }))
const pages = computed(() => Math.max(1, Math.ceil(store.total / store.pageSize)))
const pageLabel = computed(() => `Page ${store.page} of ${pages.value} · ${store.total} attempt${store.total === 1 ? '' : 's'}`)

type Row = Execution & Record<string, unknown>
const columns: Column<Row>[] = [
  { key: 'status', label: 'Status', width: 'sm' },
  { key: 'attempt', label: 'Attempt', width: 'sm', format: (e) => `${e.attempt} of ${e.max_attempts}` },
  { key: 'trigger', label: 'Trigger', width: 'sm', hideOnStack: true, format: (e) => TRIGGER_LABELS[e.trigger] ?? e.trigger },
  { key: 'scheduled_at', label: 'Scheduled', format: (e) => when(e.scheduled_at ?? e.due_at) },
  { key: 'duration_ms', label: 'Duration', width: 'sm', hideOnStack: true, format: (e) => duration(e.duration_ms) },
  { key: 'message', label: 'Message', hideOnStack: true },
]
const rows = computed(() => store.items as Row[])
</script>

<template>
  <section class="flex flex-col gap-3" data-test="task-history">
    <div class="flex flex-wrap gap-2" data-test="history-counts">
      <UiBadge color="success">{{ store.counts.succeeded ?? 0 }} succeeded</UiBadge>
      <UiBadge color="error">{{ store.counts.failed ?? 0 }} failed</UiBadge>
      <UiBadge color="neutral">{{ store.counts.other ?? 0 }} other</UiBadge>
    </div>
    <div class="grid grid-cols-1 gap-2 md:grid-cols-12 md:items-end" data-test="history-filters">
      <div class="md:col-span-3"><UiSwitch id="history-failed-only" v-model="f.failedOnly" label="Failed only" /></div>
      <div class="md:col-span-3"><UiSelect id="history-status" v-model="f.status" label="Status" :options="statusOptions" placeholder="Any" size="sm" /></div>
      <div class="md:col-span-3"><UiInput id="history-from" v-model="f.from" type="datetime-local" label="From" size="sm" @enter="load()" /></div>
      <div class="md:col-span-3"><UiInput id="history-to" v-model="f.to" type="datetime-local" label="To" size="sm" @enter="load()" /></div>
    </div>
    <div class="flex justify-end">
      <UiButton size="xs" variant="soft" icon="mdi-filter-outline" data-test="history-apply" @click="load()">Apply time range</UiButton>
    </div>
    <UiAlert v-if="store.error" kind="error">{{ store.error }}</UiAlert>
    <UiDataTable :items="rows" :columns="columns" :loading="store.loading" caption="Attempts, newest first — select one to see its message and result" empty-title="No runs yet" clickable :row-attrs="(e) => ({ 'data-test': 'execution-row-' + e.id })" data-test="history-table" @row-click="emit('open', $event.id)">
      <template #cell-status="{ row }"><UiStatusChip :status="row.status" :label="EXECUTION_STATUS_LABELS[row.status]" :colors="EXECUTION_STATUS_COLORS" /></template>
      <template #cell-message="{ row }"><span class="line-clamp-2 break-words">{{ row.message }}</span></template>
    </UiDataTable>
    <div class="flex justify-end">
      <UiPagination :has-prev="store.page > 1" :has-next="store.page < pages" :label="pageLabel" data-test="history-pager" @prev="load(store.page - 1)" @next="load(store.page + 1)" />
    </div>
  </section>
</template>
