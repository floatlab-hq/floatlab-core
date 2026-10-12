<script setup lang="ts">
import { ref, useId } from 'vue'
import type { FormSchema, FormValues } from '../forms/schema'
import UiInput from './UiInput.vue'
import UiTextarea from './UiTextarea.vue'
import UiSwitch from './UiSwitch.vue'
import UiSelect from './UiSelect.vue'
import UiButton from './UiButton.vue'
const props = withDefaults(defineProps<{ schema: FormSchema; submitLabel?: string }>(), { submitLabel: 'Save changes' })
const model = defineModel<FormValues>({ default: () => ({}) })
const emit = defineEmits<{ submit: [values: Record<string, unknown>] }>()
const id = useId()
const error = ref('')
function submit() {
  error.value = ''
  const payload: Record<string, unknown> = {}
  for (const field of props.schema.fields) {
    const value = model.value[field.key] ?? field.default
    if (value === undefined || value === '') continue
    if (field.type === 'json') {
      try { payload[field.key] = JSON.parse(String(value)) }
      catch { error.value = `${field.label}: enter valid JSON`; return }
    } else payload[field.key] = value
  }
  emit('submit', payload)
}
</script>
<template>
  <form class="api-form" @submit.prevent="submit">
    <div v-for="field in schema.fields" :key="field.key" class="form-field">
      <label :for="`${id}-${field.key}`">{{ field.label }}<span v-if="field.required"> *</span><small v-if="field.description" :id="`${id}-${field.key}-hint`">{{ field.description }}</small></label>
      <UiSwitch v-if="field.type === 'boolean'" :id="`${id}-${field.key}`" :label="field.label" :model-value="Boolean(model[field.key] ?? field.default)" @update:model-value="model = { ...model, [field.key]: $event }" />
      <UiSelect v-else-if="field.type === 'enum'" :id="`${id}-${field.key}`" :options="field.options ?? []" :model-value="String(model[field.key] ?? field.default ?? '')" :required="field.required" @update:model-value="model = { ...model, [field.key]: String($event) }" />
      <UiTextarea v-else-if="field.type === 'json' || field.type === 'multiline'" :id="`${id}-${field.key}`" :model-value="String(model[field.key] ?? '')" :required="field.required" :aria-describedby="field.description ? `${id}-${field.key}-hint` : undefined" @update:model-value="model = { ...model, [field.key]: $event }" />
      <UiInput v-else :id="`${id}-${field.key}`" variant="field" :model-value="String(model[field.key] ?? '')" :required="field.required" :aria-describedby="field.description ? `${id}-${field.key}-hint` : undefined" @update:model-value="model = { ...model, [field.key]: $event }" />
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <UiButton type="submit" variant="surface">{{ submitLabel }}</UiButton>
  </form>
</template>
<style scoped>
.api-form { padding: 16px; background: var(--surface-canvas); border-radius: var(--radius); }
.form-field { display: flex; align-items: center; justify-content: space-between; gap: 16px; min-height: 60px; margin-bottom: 16px; }
label { min-width: 0; color: var(--text-secondary); font-size: var(--text-meta); line-height: 17px; text-transform: capitalize; }
small { display: block; max-width: 48ch; font-size: 10px; line-height: 16px; text-transform: none; }
.form-field > input, .form-field > select, textarea { width: 220px; max-width: 50%; }
[role="alert"] { color: var(--status-error); font-size: var(--text-status); }
@media (max-width: 500px) { .form-field { align-items: start; flex-direction: column; gap: 8px; } .form-field > input, .form-field > select, textarea { width: 100%; max-width: 100%; } }
</style>
