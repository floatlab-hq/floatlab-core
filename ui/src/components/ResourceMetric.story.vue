<script setup lang="ts">
import { reactive } from 'vue'
import { server } from '../data'
import ResourceMetric from './ResourceMetric.vue'

const metric = reactive(structuredClone(server.metrics[0]))
</script>

<template>
  <Story title="1. Primitive/Resource Metric">
    <ResourceMetric :metric="metric" />

    <template #controls>
      <label>Value <input v-model="metric.value" /></label>
      <label>Detail <input v-model="metric.detail" /></label>
      <label>Percent <input v-model.number="metric.percent" type="range" min="0" max="100" /></label>
    </template>
  </Story>
</template>

<style scoped>
.aggregate { width: 153px; background: var(--panel); }
label { display: block; margin: 12px; }
</style>
