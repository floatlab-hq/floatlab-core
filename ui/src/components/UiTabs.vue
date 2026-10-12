<script setup lang="ts">
import { useId } from 'vue'
const model = defineModel<string>({ required: true })
const props = defineProps<{ tabs: { id: string; label: string; disabled?: boolean }[]; label: string }>()
const id = useId()
function navigate(event: KeyboardEvent, current: string) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  const enabled = props.tabs.filter(tab => !tab.disabled)
  if (!enabled.length) return
  const index = enabled.findIndex(tab => tab.id === current)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? enabled.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + enabled.length) % enabled.length
  model.value = enabled[next]!.id
  event.preventDefault()
  const list = event.currentTarget as HTMLElement
  ;(list.parentElement?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')[next])?.focus()
}
</script>
<template>
  <div class="ui-tabs">
    <div role="tablist" :aria-label="label" class="tab-list"><button v-for="tab in tabs" :id="`${id}-tab-${tab.id}`" :key="tab.id" type="button" role="tab" :aria-selected="model === tab.id" :aria-controls="`${id}-panel`" :tabindex="model === tab.id ? 0 : -1" :disabled="tab.disabled" @click="model = tab.id" @keydown="navigate($event, tab.id)">{{ tab.label }}</button></div>
    <div :id="`${id}-panel`" role="tabpanel" :aria-labelledby="`${id}-tab-${model}`" tabindex="0"><slot :active="model" /></div>
  </div>
</template>
<style scoped>
.tab-list { display: flex; gap: 2px; overflow-x: auto; background: var(--surface-default); }
.tab-list button { flex-shrink: 0; height: 40px; border: 0; border-radius: var(--radius); padding: 0 14px; color: var(--text-secondary); background: var(--surface-default); font: inherit; font-size: var(--text-control); cursor: pointer; }
.tab-list button[aria-selected="true"] { background: var(--omarchy-accent); color: var(--surface-canvas); }
.tab-list button:disabled { opacity: .5; cursor: default; }
.tab-list button:focus-visible { outline: 2px solid var(--text-primary); outline-offset: -3px; }
[role="tabpanel"] { padding-top: 18px; }
</style>
