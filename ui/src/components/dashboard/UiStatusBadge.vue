<script setup lang="ts">
import { IconPlayerPlayFilled, IconPlayerStopFilled, IconAlertTriangleFilled, IconHourglassFilled } from '@tabler/icons-vue'
import type { AppState } from '../../dashboard/types'

const props = defineProps<{ state: AppState; disabled?: boolean }>()
const icons = { Running: IconPlayerPlayFilled, Stopped: IconPlayerStopFilled, Failed: IconAlertTriangleFilled, Starting: IconHourglassFilled }
</script>

<template>
  <button type="button" class="status-badge" :class="props.state.toLowerCase()" :disabled="disabled" :aria-label="disabled ? state : `${state} — change app state`">
    <component :is="icons[state as keyof typeof icons] ?? IconHourglassFilled" :size="12" aria-hidden="true" />
    <span>{{ state }}</span>
  </button>
</template>

<style scoped>
.status-badge { display: inline-flex; align-items: center; justify-content: center; gap: 6px; height: var(--height-plate); padding: 0 var(--space-2); border: 0; border-radius: var(--radius); background: var(--status-success); color: var(--text-inverse); font-size: var(--text-status); font-weight: var(--weight-ui); line-height: var(--line-body); cursor: pointer; }
.status-badge:hover { filter: brightness(1.08); }
.status-badge:disabled { opacity: 0.5; cursor: default; }
.stopped { background: var(--omarchy-selection); color: var(--text-secondary); }
.failed { background: var(--status-error); }
.starting, .provisioning, .failingover, .restoring, .stopping, .updating, .rollingback { background: var(--status-warning); }
</style>
