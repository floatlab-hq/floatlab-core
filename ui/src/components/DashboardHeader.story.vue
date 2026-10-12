<script setup lang="ts">
import { ref } from 'vue'
import DashboardHeader from './DashboardHeader.vue'

const version = ref('FL v1.3.1')
const uptime = ref('35 days')
const lastAction = ref('none')
</script>

<template>
  <Story title="2. Pattern/Dashboard Header" responsive-disabled>
    <DashboardHeader
      :version="version"
      :uptime="uptime"
      @open-command-palette="lastAction = 'command palette'"
      @show-alerts="lastAction = 'alerts'"
      @create-app="lastAction = 'new app'"
    />
    <p class="result">Last action: {{ lastAction }}</p>

    <template #controls>
      <label>Version <input v-model="version" /></label>
      <label>Uptime <input v-model="uptime" /></label>
    </template>
  </Story>
</template>

<style scoped>
.result { width: min(1280px, 100% - 64px); margin: 0 auto; color: var(--dim); }
label { display: block; margin: 12px; }
</style>
