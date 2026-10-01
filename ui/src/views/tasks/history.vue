<script setup lang="ts">
// A task's execution history (the task drawer's "History" tab): succeeded /
// failed / other counts, a failed-only toggle, a status filter, a time range,
// server paging and whole-list header sorting (page / size / sort in the URL:
// ?runs.page=…). Selecting a row opens the execution drawer.
import { computed, onMounted, onUnmounted, reactive, watch } from 'vue'
import { UiAlert, UiBadge, UiButton, UiDataTable, UiInput, UiSelect, UiStatusChip, UiSwitch, useListQuery, type Column, type SelectOption } from '@go-tangra/ui'
import type { Execution, ExecutionFilter, ExecutionStatus } from '@/api/types'
import { RUN_LIST, useExecutions } from '@/stores/executions'
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
const lq = useListQuery('runs', RUN_LIST)
async function fetchRuns(): Promise<void> {
  const res = await store.list(filter(), lq.query.value)
  if (res?.page) lq.clampTo(res.page) // a page beyond the end answers the last page
}
watch(lq.query, () => void fetchRuns())
/** Another task or changed filters: back to page 1 (which reloads), or reload in place. */
function load(): void {
  if (lq.page.value !== 1) lq.resetPage()
  else void fetchRuns()
}
watch(() => props.taskId, () => load(), { immediate: false })
watch(() => [f.failedOnly, f.status], () => load())

// New attempts of this task appear live.
const refresh = coalesce(() => void store.reload())
let off: (() => void) | null = null
let release: (() => void) | null = null
onMounted(() => {
  void fetchRuns()
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

type Row = Execution & Record<string, unknown>
// Only the server's sort fields (RUN_LIST) are sortable.
const columns: Column<Row>[] = [
  { key: 'status', label: 'Status', width: 'sm', sortable: true },
  { key: 'attempt', label: 'Attempt', width: 'sm', format: (e) => `${e.attempt} of ${e.max_attempts}` },
  { key: 'trigger', label: 'Trigger', width: 'sm', hideOnStack: true, sortable: true, format: (e) => TRIGGER_LABELS[e.trigger] ?? e.trigger },
  { key: 'scheduled_at', label: 'Scheduled', format: (e) => when(e.scheduled_at ?? e.due_at) },
  { key: 'created_at', label: 'Recorded', hideOnStack: true, sortable: true, defaultDir: 'desc', format: (e) => when(e.created_at) },
  { key: 'duration', label: 'Duration', width: 'sm', hideOnStack: true, sortable: true, defaultDir: 'desc', format: (e) => duration(e.duration_ms) },
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
    <UiDataTable :items="rows" :columns="columns" :loading="store.loading" :total="store.total" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value" caption="Attempts — select one to see its message and result" empty-title="No runs yet" clickable :row-attrs="(e) => ({ 'data-test': 'execution-row-' + e.id })" data-test="history-table" @row-click="emit('open', $event.id)" @update:page="lq.setPage" @update:page-size="lq.setPageSize" @update:sort="lq.setSort">
      <template #cell-status="{ row }"><UiStatusChip :status="row.status" :label="EXECUTION_STATUS_LABELS[row.status]" :colors="EXECUTION_STATUS_COLORS" /></template>
      <template #cell-message="{ row }"><span class="line-clamp-2 break-words">{{ row.message }}</span></template>
    </UiDataTable>
  </section>
</template>
