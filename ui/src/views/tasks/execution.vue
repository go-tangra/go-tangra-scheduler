<script setup lang="ts">
// Execution drawer: one attempt with its status, attempt n/max, trigger (and
// who triggered a manual run), scheduled/started/finished times, duration,
// message and result. A run that is not final is followed live
// (components/ExecutionFollow.vue), so the same drawer is the follow view of
// "run now" and of a wait-for-result task.
import { UiDrawer } from '@go-tangra/ui'
import ExecutionFollow from '@/components/ExecutionFollow.vue'
import type { Execution } from '@/api/types'

withDefaults(defineProps<{
  executionId: string | null
  title?: string | undefined
  /** Follow the next attempt when this one fails with retries left (off when opened from the history). */
  followRetries?: boolean | undefined
}>(), { title: 'Execution', followRetries: true })
const emit = defineEmits<{ (e: 'close'): void; (e: 'final', v: Execution): void }>()
</script>

<template>
  <UiDrawer :model-value="executionId !== null" :title="title" size="lg" data-test="execution-drawer" @update:model-value="emit('close')">
    <ExecutionFollow v-if="executionId" :key="executionId" :execution-id="executionId" :follow-retries="followRetries" @final="emit('final', $event)" />
  </UiDrawer>
</template>
