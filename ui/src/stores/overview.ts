import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, describe } from '@/api/client'
import type { Overview } from '@/api/types'

// US6 figures for the caller's scope (GET /overview).
export const useOverview = defineStore('scheduler-overview', () => {
  const snapshot = ref<Overview | null>(null)
  const loading = ref(false)
  const error = ref('')

  async function load(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      snapshot.value = await api<Overview>('GET', 'overview')
    } catch (e) {
      snapshot.value = null
      error.value = describe(e)
    } finally {
      loading.value = false
    }
  }

  return { snapshot, loading, error, load }
})
