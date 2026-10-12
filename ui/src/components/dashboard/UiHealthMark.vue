<script setup lang="ts">
import { IconPlayerPlayFilled } from '@tabler/icons-vue'
import type { Health } from '../../dashboard/types'

defineProps<{ state: Health }>()
</script>

<template>
  <span class="health-mark" :class="state" role="img" :aria-label="state">
    <IconPlayerPlayFilled v-if="state === 'standby'" :size="12" aria-hidden="true" />
    <span v-else class="health-dot" aria-hidden="true"></span>
  </span>
</template>

<style scoped>
.health-mark { display: inline-flex; align-items: center; justify-content: center; width: 12px; height: 12px; flex: 0 0 auto; color: var(--text-secondary); }
.health-dot { width: 6px; height: 6px; border: 1px solid currentColor; border-radius: 50%; background: currentColor; }
.stopped .health-dot { background: transparent; }
.standby { color: var(--status-success); }
.degraded { color: var(--status-warning); }
.unknown { color: var(--text-placeholder); }
.unknown .health-dot { background: transparent; border-style: dashed; }
</style>
