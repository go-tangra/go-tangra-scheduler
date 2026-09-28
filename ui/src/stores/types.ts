import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, describe } from '@/api/client'
import type { TaskType } from '@/api/types'

// The task-type catalog visible to the caller (platform-scoped types only
// appear when the API returns them, i.e. for platform administrators).
export const useTypes = defineStore('scheduler-types', () => {
  const items = ref<TaskType[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref('')

  const byName = computed(() => new Map(items.value.map((t) => [t.name, t])))

  async function list(force = false): Promise<TaskType[]> {
    if (loaded.value && !force) return items.value
    loading.value = true
    error.value = ''
    try {
      const res = await api<{ items: TaskType[] }>('GET', 'task-types')
      items.value = [...(res.items ?? [])].sort((a, b) => a.module.localeCompare(b.module) || a.display_name.localeCompare(b.display_name))
      loaded.value = true
    } catch (e) {
      error.value = describe(e)
    } finally {
      loading.value = false
    }
    return items.value
  }

  const find = (name: string) => byName.value.get(name)

  return { items, loading, loaded, error, byName, list, find }
})
