<script setup lang="ts">
import { reactive, ref } from 'vue'
import { server as fixture } from '../data'
import ServerSummary from './ServerSummary.vue'

const server = reactive(structuredClone(fixture))
const settingsOpened = ref(false)
</script>

<template>
  <Story title="2. Pattern/Server Summary" responsive-disabled>
    <div class="preview">
      <ServerSummary :server="server" @settings="settingsOpened = true" />
    </div>
    <p v-if="settingsOpened">Settings requested</p>

    <template #controls>
      <label>Name <input v-model="server.name" /></label>
      <label>Temperature <input v-model="server.temperature" /></label>
      <label>Uptime <input v-model="server.uptime" /></label>
    </template>
  </Story>
</template>

<style scoped>
.preview { width: 1281px; background: var(--panel); }
p { margin: 16px; }
label { display: block; margin: 12px; }
</style>
