<script setup lang="ts">
import { onMounted, ref } from 'vue'
import UiButton from './UiButton.vue'
defineProps<{ title: string }>()
defineEmits<{ close: [] }>()
const panel = ref<HTMLElement>()
onMounted(() => panel.value?.focus({ preventScroll: true }))
</script>
<template>
  <section ref="panel" class="detail-panel" :aria-label="title" tabindex="-1" @keydown.esc.stop="$emit('close')">
    <header><slot name="identity" /><h1>{{ title }}</h1><UiButton variant="surface" aria-label="Close detail panel" @click="$emit('close')">Close ×</UiButton></header>
    <slot />
  </section>
</template>
<style scoped>
.detail-panel { min-width: 0; padding: 16px; border-radius: var(--radius); background: var(--surface-recessed); color: var(--text-secondary); outline: none; }
header { display: flex; align-items: center; gap: 8px; min-height: 40px; margin-bottom: 18px; }
h1 { flex: 1; min-width: 0; margin: 0; font-size: var(--text-heading); line-height: var(--line-heading); font-weight: var(--weight-ui); overflow-wrap: anywhere; }
</style>
