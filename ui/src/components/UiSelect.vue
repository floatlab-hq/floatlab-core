<script setup lang="ts">
const model = defineModel<string | string[]>({ default: '' })
defineProps<{ options: { value: string; label: string }[]; multiple?: boolean }>()
</script>
<template><select v-model="model" class="ui-select" :multiple="multiple"><option v-if="!multiple" value="">Select…</option><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></select></template>
<style scoped>
.ui-select { min-height: 36px; max-width: 100%; border: 1px solid transparent; border-radius: var(--radius); padding: 0 12px; color: var(--text-primary); background: var(--surface-default); font: inherit; font-size: var(--text-status); color-scheme: inherit; }
.ui-select[multiple] { padding: 8px 12px; }
.ui-select:focus-visible { outline: 2px solid var(--omarchy-accent); outline-offset: 2px; }
.ui-select:disabled { opacity: .5; }
</style>
