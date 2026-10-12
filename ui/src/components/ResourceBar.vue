<script setup lang="ts">
import type { ResourceValue } from '../types'
import ResourceTrack from './ResourceTrack.vue'

withDefaults(defineProps<{ value: ResourceValue; danger?: boolean }>(), { danger: false })
</script>

<template>
  <div class="cell-metric" :class="{ danger }">
    <span>{{ value.label }}</span>
    <ResourceTrack :percent="value.percent" :danger="danger" />
  </div>
</template>
