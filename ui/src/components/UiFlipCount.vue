<script setup lang="ts">
import { ref, watch } from 'vue'
const props = defineProps<{ count: number }>()
const revision = ref(0)
watch(() => props.count, (count, previous) => { if (count > previous) revision.value++ })
</script>

<template>
  <span class="flip-count" aria-hidden="true">
    <span :key="revision" class="flip-count-face" :class="{ arriving: revision > 0 }">{{ count > 99 ? '99+' : count }}</span>
  </span>
</template>

<style scoped>
.flip-count { position: relative; display: inline-grid; place-items: center; width: 26px; height: 24px; isolation: isolate; perspective: 100px; }
.flip-count::before, .flip-count::after { content: ''; position: absolute; inset: 0; border: 1px solid currentColor; border-radius: var(--radius); background: var(--surface-default); z-index: -1; }
.flip-count::before { transform: translate(3px, -3px); opacity: .25; }
.flip-count::after { transform: translate(1.5px, -1.5px); opacity: .5; }
.flip-count-face { display: grid; place-items: center; width: 100%; height: 100%; border: 1px solid currentColor; border-radius: var(--radius); background: var(--surface-default); font-size: var(--text-meta); font-variant-numeric: tabular-nums; line-height: 1; transform-origin: center; backface-visibility: hidden; }
.flip-count-face::after { content: ''; position: absolute; inset: 50% 0 auto; border-top: 1px solid currentColor; opacity: .12; pointer-events: none; }
.arriving { animation: count-flip 420ms cubic-bezier(.22,1,.36,1); }
@keyframes count-flip { 0% { transform: translateY(-3px) rotateX(-80deg); opacity: .3; } 65% { transform: translateY(1px) rotateX(8deg); opacity: 1; } 100% { transform: translateY(0) rotateX(0); } }
@media (prefers-reduced-motion: reduce) { .arriving { animation: none; } .flip-count::before, .flip-count::after { transition: none; } }
</style>
