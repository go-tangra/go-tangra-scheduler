<script setup lang="ts">
// Scheduled tasks: a filter bar (text, kind, state, type, validity), a
// server-paged table (type and module, kind, the schedule in plain words,
// next run, state with validity warnings, last result, run count), row
// actions (edit, history, start/stop/restart, run now / run again, cancel,
// delete) and a bulk bar (start/stop/restart every periodic task in scope).
// Actions the user may not perform are hidden (CASL abilities from the shell).
import { computed, inject, onMounted, onUnmounted, reactive, ref } from 'vue'
import { routeLocationKey } from 'vue-router'
import { useAbility } from '@casl/vue'
import { UiAlert, UiBadge, UiButton, UiCard, UiDataTable, UiInput, UiLiveIndicator, UiPage, UiPagination, UiSelect, UiStatusChip, UiTooltip, useConfirm, useToast, type Column, type SelectOption } from '@go-tangra/ui'
import { describe } from '@/api/client'
import type { BulkAction, Kind, Task, TaskState, Validity } from '@/api/types'
import { useTasks, type ControlAction } from '@/stores/tasks'
import { useTypes } from '@/stores/types'
import { coalesce, useLive } from '@/stores/live'
import { EXECUTION_STATUS_COLORS, KINDS, KIND_LABELS, STATE_COLORS, STATE_LABELS, TASK_STATES, VALIDITIES, VALIDITY_LABELS, isOneShot, statusLabel } from '@/schemas'
import { describeSchedule } from '@/utils/cron'
import { when } from '@/utils/format'
import TaskDrawer from './drawer.vue'
import ExecutionDrawer from './execution.vue'

const store = useTasks()
const types = useTypes()
const live = useLive()
const ability = useAbility()
const confirm = useConfirm()
const toast = useToast()

const canCreate = computed(() => ability.can('create', 'SchedulerTask'))
const canUpdate = computed(() => ability.can('update', 'SchedulerTask'))
const canDelete = computed(() => ability.can('delete', 'SchedulerTask'))
const canControl = computed(() => ability.can('control', 'SchedulerTask'))
const canHistory = computed(() => ability.can('read', 'SchedulerExecution'))

// --- filters ---
// The overview links here with ?q=<task name>.
const route = inject(routeLocationKey, null)
const f = reactive({ q: typeof route?.query.q === 'string' ? route.query.q : '', kind: '' as Kind | '', state: '' as TaskState | '', type: '', validity: '' as Validity | '' })
function apply(p = 1): void {
  void store.list({ q: f.q.trim() || undefined, kind: f.kind || undefined, state: f.state || undefined, type: f.type || undefined, validity: f.validity || undefined }, p)
}
function setFilter(key: 'kind' | 'state' | 'type' | 'validity', v: unknown): void {
  ;(f as Record<string, unknown>)[key] = typeof v === 'string' ? v : ''
  apply()
}
const kindOptions: SelectOption[] = KINDS.map((k) => ({ title: KIND_LABELS[k], value: k }))
const stateOptions: SelectOption[] = TASK_STATES.map((s) => ({ title: STATE_LABELS[s], value: s }))
const validityOptions: SelectOption[] = VALIDITIES.map((v) => ({ title: VALIDITY_LABELS[v], value: v }))
const typeOptions = computed<SelectOption[]>(() => types.items.map((t) => ({ title: `${t.display_name} (${t.module})`, value: t.name })))

// --- live refresh ---
const refresh = coalesce(() => void store.reload())
let release: (() => void) | null = null
let off: (() => void) | null = null
onMounted(() => {
  apply()
  void types.list()
  release = live.connect()
  off = live.on(() => refresh.trigger())
})
onUnmounted(() => {
  off?.()
  release?.()
  refresh.cancel()
})

// --- paging ---
const pages = computed(() => Math.max(1, Math.ceil(store.total / store.pageSize)))
const pageLabel = computed(() => `Page ${store.page} of ${pages.value} · ${store.total} task${store.total === 1 ? '' : 's'}`)

// --- drawers ---
const drawerOpen = ref(false)
const drawerTask = ref<Task | null>(null)
const drawerTab = ref<'settings' | 'history'>('settings')
function openTask(t: Task | null, tab: 'settings' | 'history' = 'settings'): void {
  drawerTask.value = t
  drawerTab.value = tab
  drawerOpen.value = true
}
function onSaved(t: Task): void {
  toast.show({ kind: 'success', title: drawerTask.value ? 'Task saved' : 'Task created' })
  void store.reload()
  if (drawerTask.value) drawerTask.value = t
}
const followId = ref<string | null>(null)
const followRetries = ref(true)
function follow(id: string, retries = true): void {
  followRetries.value = retries
  followId.value = id
}

// --- row actions ---
const error = ref('')
const busy = ref('')
const periodic = (t: Task) => t.kind === 'periodic'
const canStart = (t: Task) => periodic(t) && t.state === 'stopped'
const canStop = (t: Task) => periodic(t) && t.state === 'enabled'
const canCancel = (t: Task) => isOneShot(t.kind) && t.status === 'active'
const runLabel = (t: Task) => (isOneShot(t.kind) && t.state === 'completed' ? 'Run again' : 'Run now')
const canRun = (t: Task) => t.state !== 'cancelled'

const DONE: Record<ControlAction, string> = { start: 'Task started', stop: 'Task stopped', restart: 'Task restarted', cancel: 'Task cancelled' }
async function control(t: Task, action: ControlAction): Promise<void> {
  if (action === 'cancel' && !(await confirm.ask({ title: `Cancel ${t.name}?`, text: 'The task will not run. This cannot be undone.', danger: true, confirmLabel: 'Cancel task', cancelLabel: 'Keep' }))) return
  error.value = ''
  busy.value = t.id
  try {
    await store.control(t.id, action)
    toast.show({ kind: 'success', title: DONE[action] })
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = ''
  }
}
async function run(t: Task): Promise<void> {
  error.value = ''
  busy.value = t.id
  try {
    follow(await store.run(t.id))
    void store.reload()
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = ''
  }
}
async function remove(t: Task): Promise<void> {
  if (!(await confirm.ask({ title: `Delete ${t.name}?`, text: 'Queued runs are cancelled; a run in progress finishes and is recorded. The history is deleted with the task.', danger: true, confirmLabel: 'Delete' }))) return
  error.value = ''
  try {
    await store.remove(t.id)
    toast.show({ kind: 'success', title: 'Task deleted' })
  } catch (e) {
    error.value = describe(e)
  }
}

// --- bulk ---
const BULK: Record<BulkAction, { title: string; text: string; done: string }> = {
  start: { title: 'Start all periodic tasks?', text: 'Every stopped periodic task in your scope is enabled.', done: 'started' },
  stop: { title: 'Stop all periodic tasks?', text: 'Every enabled periodic task in your scope stops until started again.', done: 'stopped' },
  restart: { title: 'Restart all periodic tasks?', text: 'Every enabled periodic task in your scope is stopped and started again.', done: 'restarted' },
}
const bulkResult = ref('')
const bulkBusy = ref<BulkAction | ''>('')
async function bulk(action: BulkAction): Promise<void> {
  const b = BULK[action]
  if (!(await confirm.ask({ title: b.title, text: b.text, confirmLabel: action[0]!.toUpperCase() + action.slice(1) + ' all', danger: action === 'stop' }))) return
  error.value = ''
  bulkBusy.value = action
  try {
    const n = await store.bulk(action)
    bulkResult.value = `${n} task${n === 1 ? '' : 's'} ${b.done}.`
    toast.show({ kind: 'success', title: bulkResult.value })
    void store.reload()
  } catch (e) {
    error.value = describe(e)
  } finally {
    bulkBusy.value = ''
  }
}

// --- table ---
type Row = Task & Record<string, unknown>
const columns: Column<Row>[] = [
  { key: 'name', label: 'Name' },
  { key: 'type', label: 'Type' },
  { key: 'kind', label: 'Kind', width: 'sm', hideOnStack: true, format: (t) => KIND_LABELS[t.kind] },
  { key: 'schedule', label: 'Schedule' },
  { key: 'next_run_at', label: 'Next run', format: (t) => when(t.next_run_at) || '—' },
  { key: 'state', label: 'State' },
  { key: 'last', label: 'Last result', hideOnStack: true },
  { key: 'run_count', label: 'Runs', width: 'sm', align: 'end', hideOnStack: true, format: (t) => String(t.run_count ?? 0) },
]
const rows = computed(() => store.items as Row[])
const schedule = (t: Task) => (t.kind === 'periodic' ? describeSchedule(t.cron, t.timezone || 'UTC') : t.run_at ? 'once at ' + when(t.run_at) : 'once')
const lastStatusColor = (s: string | undefined) => EXECUTION_STATUS_COLORS[s as keyof typeof EXECUTION_STATUS_COLORS] ?? 'neutral'
</script>

<template>
  <UiPage title="Scheduled tasks">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions>
      <UiButton v-if="canCreate" icon="mdi-plus" data-test="task-new" @click="openTask(null)">New task</UiButton>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="store.reload()" />
    </template>

    <div class="flex min-w-0 flex-col gap-3">
      <UiCard>
        <div class="grid grid-cols-2 gap-2 md:grid-cols-12 md:items-end" data-test="task-filters">
          <div class="col-span-2 md:col-span-4"><UiInput id="task-filter-q" v-model="f.q" label="Search (name / type)" type="search" size="sm" data-test="task-filter-q" @enter="apply()" /></div>
          <div class="md:col-span-2"><UiSelect id="task-filter-kind" :model-value="f.kind" label="Kind" :options="kindOptions" placeholder="Any" size="sm" @update:model-value="setFilter('kind', $event)" /></div>
          <div class="md:col-span-2"><UiSelect id="task-filter-state" :model-value="f.state" label="State" :options="stateOptions" placeholder="Any" size="sm" @update:model-value="setFilter('state', $event)" /></div>
          <div class="md:col-span-2"><UiSelect id="task-filter-type" :model-value="f.type" label="Type" :options="typeOptions" placeholder="Any" size="sm" @update:model-value="setFilter('type', $event)" /></div>
          <div class="md:col-span-2"><UiSelect id="task-filter-validity" :model-value="f.validity" label="Validity" :options="validityOptions" placeholder="Any" size="sm" @update:model-value="setFilter('validity', $event)" /></div>
        </div>
      </UiCard>

      <div v-if="canControl" class="flex flex-wrap items-center gap-2" role="group" aria-label="All periodic tasks" data-test="bulk-bar">
        <span class="text-sm text-base-content/70">All periodic tasks:</span>
        <UiButton size="sm" variant="soft" icon="mdi-play" :loading="bulkBusy === 'start'" data-test="bulk-start" @click="bulk('start')">Start all</UiButton>
        <UiButton size="sm" variant="soft" icon="mdi-stop" :loading="bulkBusy === 'stop'" data-test="bulk-stop" @click="bulk('stop')">Stop all</UiButton>
        <UiButton size="sm" variant="soft" icon="mdi-restart" :loading="bulkBusy === 'restart'" data-test="bulk-restart" @click="bulk('restart')">Restart all</UiButton>
        <span v-if="bulkResult" class="text-sm" role="status" data-test="bulk-result">{{ bulkResult }}</span>
      </div>

      <UiAlert v-if="error" kind="error" data-test="task-error">{{ error }}</UiAlert>
      <UiAlert v-if="store.error" kind="error">{{ store.error }}</UiAlert>

      <UiCard :padded="false">
        <UiDataTable :items="rows" :columns="columns" :loading="store.loading" caption="Scheduled tasks — select one to see its settings and history" empty-title="No tasks" empty-text="Create a task to run a module's job on a schedule or once." clickable :row-attrs="(t) => ({ 'data-test': 'task-row-' + t.id })" data-test="tasks-table" @row-click="openTask($event)">
          <template #cell-name="{ row }">
            <span class="font-medium">{{ row.name }}</span>
            <UiBadge v-if="row.scope === 'platform'" size="xs" color="secondary" class="ms-1">platform</UiBadge>
          </template>
          <template #cell-type="{ row }">
            <span>{{ row.type_display_name || row.type_name }}</span>
            <span class="block text-xs text-base-content/70">{{ row.module }}</span>
          </template>
          <template #cell-schedule="{ row }"><span :data-test="'task-schedule-' + row.id">{{ schedule(row) }}</span></template>
          <template #cell-state="{ row }">
            <div class="flex flex-wrap items-center gap-1">
              <UiStatusChip :status="row.state" :label="STATE_LABELS[row.state]" :colors="STATE_COLORS" />
              <UiTooltip v-if="row.validity !== 'ok'" :text="row.validity_message || VALIDITY_LABELS[row.validity]">
                <UiBadge size="xs" color="warning" :data-test="'task-validity-' + row.id">{{ VALIDITY_LABELS[row.validity] }}</UiBadge>
              </UiTooltip>
            </div>
          </template>
          <template #cell-last="{ row }">
            <UiTooltip v-if="row.last_status" :text="row.last_message || statusLabel(row.last_status)">
              <UiBadge size="xs" :color="lastStatusColor(row.last_status)" :data-test="'task-last-' + row.id">{{ statusLabel(row.last_status) }}</UiBadge>
            </UiTooltip>
            <span v-else class="text-base-content/70">—</span>
            <span v-if="row.last_run_at" class="block text-xs text-base-content/70">{{ when(row.last_run_at) }}</span>
          </template>
          <template #actions="{ row }">
            <div class="flex flex-wrap justify-end gap-0.5" @click.stop>
              <UiButton v-if="canUpdate" size="xs" variant="text" icon="mdi-pencil-outline" icon-only label="Edit task" :data-test="'task-edit-' + row.id" @click="openTask(row)" />
              <UiButton v-if="canHistory" size="xs" variant="text" icon="mdi-history" icon-only label="History" :data-test="'task-history-' + row.id" @click="openTask(row, 'history')" />
              <template v-if="canControl">
                <UiButton v-if="canStart(row)" size="xs" variant="text" color="success" icon="mdi-play" icon-only label="Start" :disabled="busy === row.id" :data-test="'task-start-' + row.id" @click="control(row, 'start')" />
                <UiButton v-if="canStop(row)" size="xs" variant="text" icon="mdi-stop" icon-only label="Stop" :disabled="busy === row.id" :data-test="'task-stop-' + row.id" @click="control(row, 'stop')" />
                <UiButton v-if="canStop(row)" size="xs" variant="text" icon="mdi-restart" icon-only label="Restart" :disabled="busy === row.id" :data-test="'task-restart-' + row.id" @click="control(row, 'restart')" />
                <UiButton v-if="canRun(row)" size="xs" variant="text" color="primary" icon="mdi-play-circle-outline" icon-only :label="runLabel(row)" :disabled="busy === row.id" :data-test="'task-run-' + row.id" @click="run(row)" />
                <UiButton v-if="canCancel(row)" size="xs" variant="text" color="warning" icon="mdi-cancel" icon-only label="Cancel task" :disabled="busy === row.id" :data-test="'task-cancel-' + row.id" @click="control(row, 'cancel')" />
              </template>
              <UiButton v-if="canDelete" size="xs" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Delete task" :data-test="'task-delete-' + row.id" @click="remove(row)" />
            </div>
          </template>
        </UiDataTable>
      </UiCard>
      <div class="flex justify-end">
        <UiPagination :has-prev="store.page > 1" :has-next="store.page < pages" :label="pageLabel" data-test="task-pager" @prev="store.list(store.filter, store.page - 1)" @next="store.list(store.filter, store.page + 1)" />
      </div>
    </div>

    <TaskDrawer :open="drawerOpen" :task="drawerTask" :initial-tab="drawerTab" @close="drawerOpen = false" @saved="onSaved" @follow="follow($event)" @open-execution="follow($event, false)" />
    <ExecutionDrawer :execution-id="followId" :follow-retries="followRetries" @close="followId = null" @final="store.reload()" />
  </UiPage>
</template>
