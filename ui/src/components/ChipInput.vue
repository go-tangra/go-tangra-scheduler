<script setup lang="ts">
// A list of text values edited as chips: type a value and press Enter (or a
// comma) to add it; each chip has a remove button. Used for JSON Schema
// "array of strings" payload properties.
import { computed, ref } from 'vue'
import { UiField, UiIcon } from '@go-tangra/ui'

const props = defineProps<{
  id: string
  label: string
  modelValue?: unknown
  hint?: string | undefined
  error?: string | undefined
  required?: boolean | undefined
  disabled?: boolean | undefined
  maxItems?: number | undefined
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

const values = computed<string[]>(() => (Array.isArray(props.modelValue) ? props.modelValue.map(String) : []))
const draft = ref('')
const full = computed(() => props.maxItems !== undefined && values.value.length >= props.maxItems)

function add(): void {
  const v = draft.value.trim().replace(/,$/, '').trim()
  draft.value = ''
  if (!v || values.value.includes(v) || full.value) return
  emit('update:modelValue', [...values.value, v])
}
function remove(i: number): void {
  emit('update:modelValue', values.value.filter((_, j) => j !== i))
}
function onKey(e: KeyboardEvent): void {
  if (e.key === 'Enter' || e.key === ',') {
    e.preventDefault()
    add()
  } else if (e.key === 'Backspace' && draft.value === '' && values.value.length) {
    remove(values.value.length - 1)
  }
}
</script>

<template>
  <UiField :id="id" :label="label" :hint="hint ?? 'Press Enter to add a value.'" :error="error" :required="required">
    <template #default="{ describedBy, invalid }">
      <div class="input flex h-auto min-h-10 flex-wrap items-center gap-1 py-1" :class="invalid ? 'is-invalid' : ''">
        <ul v-if="values.length" class="flex flex-wrap gap-1" :aria-label="label + ' values'">
          <li v-for="(v, i) in values" :key="v" class="badge badge-soft badge-primary gap-1" :data-test="id + '-chip-' + i">
            <span class="break-all">{{ v }}</span>
            <button type="button" class="cursor-pointer" :aria-label="'Remove ' + v" :disabled="disabled" @click="remove(i)"><UiIcon name="mdi-close" size="xs" /></button>
          </li>
        </ul>
        <input
          :id="id"
          v-model="draft"
          :data-field="id"
          type="text"
          class="min-w-24 grow bg-transparent outline-none"
          :disabled="disabled || full"
          :aria-invalid="invalid || undefined"
          :aria-describedby="describedBy"
          @keydown="onKey"
          @blur="add"
        >
      </div>
    </template>
  </UiField>
</template>
