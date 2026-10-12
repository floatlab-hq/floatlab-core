<script setup lang="ts">
import { publicAsset } from '../public-asset'
import type { Server } from '../types'
import ResourceMetric from './ResourceMetric.vue'
import ResourceTrack from './ResourceTrack.vue'
import UiButton from './UiButton.vue'

defineEmits<{ settings: [] }>()
defineProps<{ server: Server }>()
</script>

<template>
  <header class="server-summary" aria-labelledby="server-name">
    <div class="server-identity">
      <svg class="server-mark" viewBox="0 0 56 48" aria-hidden="true">
        <path fill="var(--line)" d="M0 0h56v14H0zm0 17h56v14H0zm0 17h56v14H0z" />
        <path fill="var(--green)" d="M7 5h5v4H7zm0 17h5v4H7zm0 17h5v4H7z" />
      </svg>
      <div>
        <h1 id="server-name">{{ server.name }}</h1>
        <p>{{ server.temperature }}<span>{{ server.uptime }} uptime</span></p>
        <UiButton variant="outline" size="small" @click="$emit('settings')">Settings</UiButton>
      </div>
    </div>

    <div class="aggregate-grid">
      <ResourceMetric v-for="metric in server.metrics" :key="metric.id" :metric="metric" />

      <article class="aggregate network" data-series="network">
        <div class="hardware-badge network-spec">
          <i></i>
          <img :src="publicAsset('/assets/network-port-idle.svg')" alt="" />
          <img :src="publicAsset('/assets/network-port-active.svg')" alt="" />
        </div>
        <div class="network-values">
          <span><b>↑</b>{{ server.network.up.label }}</span>
          <span><b>↓</b>{{ server.network.down.label }}</span>
        </div>
        <div class="network-charts">
          <ResourceTrack data-series="networkUp" :percent="server.network.up.percent" :history="[]" reverse />
          <ResourceTrack data-series="networkDown" :percent="server.network.down.percent" :history="[]" />
        </div>
      </article>
    </div>
  </header>
</template>
