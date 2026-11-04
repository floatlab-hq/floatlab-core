<script setup lang="ts">
import { computed } from 'vue'
import type { AppState } from '../../dashboard/types'
import UiPlateCard from '../UiPlateCard.vue'
const props = defineProps<{ state: AppState; disabled?: boolean }>()
const running = computed(() => ['Running', 'RunningPrimary', 'RunningBackup'].includes(props.state))
</script>
<template>
  <UiPlateCard class="status-badge" :class="state.toLowerCase()" :label="running ? 'Running' : state === 'Idle' ? 'Stopped' : state" :active="running" :tone="state === 'Failed' ? 'error' : 'default'" :disabled="disabled" :aria-label="disabled ? state : `${state} — change app state`" />
</template>
