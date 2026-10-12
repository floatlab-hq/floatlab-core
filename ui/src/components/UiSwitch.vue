<script setup lang="ts">
const model = defineModel<boolean>({ default: false })
defineProps<{ label: string; disabled?: boolean }>()
</script>
<template>
  <button type="button" class="ui-switch" role="switch" :aria-label="label" :aria-checked="model" :disabled="disabled" @click="model = !model">
    <span>{{ model ? 'On' : 'Off' }}</span><span class="switch-track" :class="{ on: model }"><span /></span>
  </button>
</template>
<style scoped>
.ui-switch { display: inline-flex; align-items: center; gap: 8px; min-height: 36px; padding: 0; border: 0; color: var(--text-secondary); background: transparent; font: inherit; font-size: var(--text-meta); cursor: pointer; }
.switch-track { display: flex; align-items: center; width: 36px; height: 20px; padding: 3px; border-radius: var(--radius); background: var(--surface-recessed); }
.switch-track > span { width: 14px; height: 14px; border-radius: var(--radius); background: var(--text-secondary); transition: transform 180ms ease; }
.switch-track.on { background: var(--omarchy-accent); }
.switch-track.on > span { background: var(--surface-canvas); transform: translateX(16px); }
.ui-switch:disabled { opacity: .5; cursor: default; }
.ui-switch:focus-visible { outline: 2px solid var(--omarchy-accent); outline-offset: 3px; }
@media (prefers-reduced-motion: reduce) { .switch-track > span { transition: none; } }
</style>
