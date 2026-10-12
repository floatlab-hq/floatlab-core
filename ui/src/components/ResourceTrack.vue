<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{ percent: number; history?: number[]; danger?: boolean; reverse?: boolean; compact?: boolean }>(),
  { history: undefined, danger: false, reverse: false, compact: false },
)
const clamp = (value: number) => Math.min(100, Math.max(0, value))
const width = computed(() => clamp(props.percent))
const historyClip = computed(() => {
  if (!props.history) return undefined
  const [a = 0, b = 0, c = 0, d = 0] = props.history.slice(0, 4).map(clamp)

  return `polygon(0 0, ${a}% 0, ${a}% 25%, ${b}% 25%, ${b}% 50%, ${c}% 50%, ${c}% 75%, ${d}% 75%, ${d}% 100%, 0 100%)`
})
</script>

<template>
  <div v-if="history" class="chart-stack" :class="{ danger, reverse, compact }">
    <div class="history" :style="{ clipPath: historyClip }"></div>
    <i class="live-track"><b :style="{ width: `${width}%` }"></b></i>
  </div>
  <i v-else class="resource-track" :class="{ danger, reverse, compact }">
    <b :style="{ width: `${width}%` }"></b>
  </i>
</template>
