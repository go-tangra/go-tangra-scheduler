<script setup lang="ts">
// Scheduler dashboard (US6): task counts by state and validity, runs and
// failures in the last 24 hours, the next runs due and the failing tasks
// (each linking to the task list filtered to it). Refreshed live.
import { computed, onMounted, onUnmounted } from 'vue'
import { RouterLink } from 'vue-router'
import { UiAlert, UiBadge, UiButton, UiCard, UiEmptyState, UiLiveIndicator, UiPage, UiStatGrid, UiStatTile } from '@go-tangra/ui'
import { useOverview } from '@/stores/overview'
import { coalesce, useLive } from '@/stores/live'
import { EXECUTION_STATUS_COLORS, statusLabel } from '@/schemas'
import { when } from '@/utils/format'

const store = useOverview()
const live = useLive()
const refresh = coalesce(() => void store.load(), 1000)
let release: (() => void) | null = null
let off: (() => void) | null = null
onMounted(() => {
  void store.load()
  release = live.connect()
  off = live.on(() => refresh.trigger())
})
onUnmounted(() => {
  off?.()
  release?.()
  refresh.cancel()
})

const s = computed(() => store.snapshot)
const t = computed(() => s.value?.tasks ?? {})
const nextDue = computed(() => s.value?.next_due ?? [])
const failing = computed(() => s.value?.failing ?? [])
const taskLink = (name?: string) => ({ path: '/scheduler', query: name ? { q: name } : {} })
const color = (st?: string) => EXECUTION_STATUS_COLORS[st as keyof typeof EXECUTION_STATUS_COLORS] ?? 'error'
</script>

<template>
  <UiPage title="Scheduler dashboard">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="store.load()" />
    </template>
    <UiAlert v-if="store.error" kind="error" class="mb-3">{{ store.error }}</UiAlert>

    <h2 class="mb-2 text-sm font-semibold">Tasks</h2>
    <UiStatGrid class="mb-4" :cols="6" data-test="overview-tasks">
      <UiStatTile title="Enabled" :value="t.enabled ?? 0" icon="mdi-play-circle-outline" color="success" />
      <UiStatTile title="Stopped" :value="t.stopped ?? 0" icon="mdi-stop-circle-outline" color="neutral" />
      <UiStatTile title="Completed" :value="t.completed ?? 0" icon="mdi-check-circle-outline" color="info" />
      <UiStatTile title="Cancelled" :value="t.cancelled ?? 0" icon="mdi-cancel" color="neutral" />
      <UiStatTile title="Type unavailable" :value="t.type_unavailable ?? 0" icon="mdi-puzzle-remove-outline" color="warning" />
      <UiStatTile title="Payload invalid" :value="t.payload_invalid ?? 0" icon="mdi-alert-outline" color="warning" />
    </UiStatGrid>

    <h2 class="mb-2 text-sm font-semibold">Last 24 hours</h2>
    <UiStatGrid class="mb-4" :cols="2" data-test="overview-runs">
      <UiStatTile title="Runs" :value="s?.runs_24h ?? 0" icon="mdi-run" color="primary" subtitle="attempts started" />
      <UiStatTile title="Failures" :value="s?.failures_24h ?? 0" icon="mdi-alert-circle-outline" color="error" subtitle="failed or timed out" />
    </UiStatGrid>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <UiCard title="Next runs due" data-test="overview-next">
        <UiEmptyState v-if="!nextDue.length" title="Nothing scheduled" />
        <ul v-else class="divide-y divide-base-300">
          <li v-for="n in nextDue" :key="n.task_id" class="flex flex-wrap items-baseline gap-2 py-2">
            <RouterLink :to="taskLink(n.name)" class="link link-hover font-medium" :data-test="'overview-next-' + n.task_id">{{ n.name || n.task_id }}</RouterLink>
            <span class="grow text-end text-sm text-base-content/70">{{ when(n.next_run_at) }}</span>
          </li>
        </ul>
      </UiCard>
      <UiCard title="Failing tasks" data-test="overview-failing">
        <UiEmptyState v-if="!failing.length" title="No failing tasks" />
        <ul v-else class="divide-y divide-base-300">
          <li v-for="x in failing" :key="x.task_id" class="flex flex-col gap-1 py-2">
            <div class="flex flex-wrap items-center gap-2">
              <RouterLink :to="taskLink(x.name)" class="link link-hover font-medium" :data-test="'overview-failing-' + x.task_id">{{ x.name || x.task_id }}</RouterLink>
              <UiBadge size="xs" :color="color(x.last_status)">{{ statusLabel(x.last_status) || 'Failed' }}</UiBadge>
              <span class="grow text-end text-xs text-base-content/70">{{ when(x.last_run_at) }}</span>
            </div>
            <p v-if="x.last_message" class="text-sm break-words text-base-content/80">{{ x.last_message }}</p>
          </li>
        </ul>
      </UiCard>
    </div>
  </UiPage>
</template>
