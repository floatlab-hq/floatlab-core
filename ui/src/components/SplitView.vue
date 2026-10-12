<script setup lang="ts">
withDefaults(defineProps<{ open?: boolean }>(), { open: false })
defineEmits<{ closed: [] }>()
</script>
<template>
  <div class="split-view" :class="{ 'is-open': open }">
    <div class="split-main"><slot /></div>
    <Transition name="detail-slide" @after-leave="$emit('closed')"><div v-if="open" class="split-detail"><slot name="detail" /></div></Transition>
  </div>
</template>
<style scoped>
.split-view { display: grid; grid-template-columns: minmax(0, 1fr) 0fr; gap: 0; align-items: start; transition: grid-template-columns 280ms cubic-bezier(.22,1,.36,1), gap 280ms ease; }
.split-view.is-open { grid-template-columns: minmax(320px, .46fr) minmax(0, 1fr); gap: 18px; }
.split-main, .split-detail { min-width: 0; }
.detail-slide-enter-active, .detail-slide-leave-active { transition: transform 280ms cubic-bezier(.22,1,.36,1), opacity 200ms ease; }
.detail-slide-enter-from, .detail-slide-leave-to { transform: translateX(32px); opacity: 0; }
@media (max-width: 900px) { .split-view.is-open { grid-template-columns: minmax(0,1fr); gap: 0; } .is-open .split-main { display: none; } }
@media (prefers-reduced-motion: reduce) { .split-view, .detail-slide-enter-active, .detail-slide-leave-active { transition: none; } .detail-slide-enter-from, .detail-slide-leave-to { transform: none; } }
</style>
