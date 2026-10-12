<script setup lang="ts">
import ResourceTrack from '../ResourceTrack.vue'

defineProps<{ cpu: string; memory?: number; disk?: number }>()
</script>

<template>
  <span class="telemetry">
    <span :aria-label="`CPU history ${cpu}`">C {{ cpu }}</span>
    <span class="telemetry-meter" :aria-label="memory === undefined ? 'Memory unavailable' : `Memory ${memory}%`">M <ResourceTrack v-if="memory !== undefined" :percent="memory" compact /><span v-else>—</span></span>
    <span class="telemetry-meter" :aria-label="disk === undefined ? 'Disk unavailable' : `Disk ${disk}%`">D <ResourceTrack v-if="disk !== undefined" :percent="disk" compact /><span v-else>—</span></span>
  </span>
</template>

<style scoped>
.telemetry { display: inline-flex; align-items: center; gap: 6px; color: var(--text-secondary); font-size: var(--text-meta); font-weight: var(--weight-ui); line-height: var(--line-meta); white-space: nowrap; }
.telemetry-meter { display: inline-flex; align-items: center; gap: var(--space-1); }
</style>
