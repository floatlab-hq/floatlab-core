<script setup lang="ts">
const model = defineModel<string>({ default: '' })
</script>
<template><textarea v-model="model" class="ui-textarea" /></template>
<style scoped>
.ui-textarea { min-height: 72px; border: 1px solid var(--line); border-radius: var(--radius); padding: 8px 12px; color: var(--text-primary); background: var(--surface-default); font: inherit; font-size: var(--text-status); }
.ui-textarea:focus-visible { outline: 2px solid var(--omarchy-accent); outline-offset: 2px; }
.ui-textarea:disabled { opacity: .5; }
</style>
