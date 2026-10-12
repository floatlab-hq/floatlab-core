<script setup lang="ts">
import { publicAsset } from '../public-asset'
import type { Metric } from '../types'
import ResourceTrack from './ResourceTrack.vue'

defineProps<{ metric: Metric }>()
</script>

<template>
  <article class="aggregate" :data-series="metric.id">
    <div v-if="metric.id === 'cpu'" class="hardware-badge cpu-spec">14C/24T</div>
    <div v-else-if="metric.id === 'memory'" class="hardware-badge ram-spec">
      <img :src="publicAsset('/assets/ram.svg')" alt="" />
      <img :src="publicAsset('/assets/ram.svg')" alt="" />
    </div>
    <div v-else-if="metric.id === 'storage'" class="hardware-badge storage-spec">
      <img :src="publicAsset('/assets/storage-alert.svg')" alt="" />
      <img :src="publicAsset('/assets/storage-ok.svg')" alt="" />
      <img :src="publicAsset('/assets/storage-idle.svg')" alt="" />
    </div>

    <div class="metric-meta">
      <strong>{{ metric.value }}</strong>
      <span>{{ metric.detail }}</span>
    </div>
    <ResourceTrack :percent="metric.percent" :history="metric.history" />
  </article>
</template>
