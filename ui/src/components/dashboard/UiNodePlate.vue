<script setup lang="ts">
import { computed } from 'vue'
import { activeTheme } from '../../themes'
import { nodeCode } from '../../dashboard/node-code'
import { generate } from '../../vendor/avatar-engine'

const props = defineProps<{ name: string }>()
const image = computed(() => {
  const svg = generate(props.name, { ink: activeTheme.value.colors.lighter_background, paper: activeTheme.value.colors.foreground }).svg
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
})
</script>

<template>
  <span class="node-plate" :title="name" :aria-label="`Node ${name}`">
    <img :src="image" width="24" height="24" alt="" />
    <span>{{ nodeCode(name) }}</span>
  </span>
</template>

<style scoped>
.node-plate { display: inline-flex; align-items: center; justify-content: center; gap: var(--space-1); height: var(--height-plate); padding: 0 var(--space-2); border-radius: var(--radius); color: var(--text-inverse); background: var(--omarchy-foreground); font-size: var(--text-control); font-weight: var(--weight-ui); line-height: var(--line-body); white-space: nowrap; }
.node-plate img { display: block; flex: 0 0 auto; }
</style>
