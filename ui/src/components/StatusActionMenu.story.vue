<script setup lang="ts">
import { ref } from 'vue'
import type { Operation, Status } from '../types'
import StatusActionMenu from './StatusActionMenu.vue'

const status = ref<Status>('Running')
const pending = ref<Operation>()
</script>

<template>
  <Story title="2. Pattern/Status Action Menu">
    <div class="preview">
      <StatusActionMenu :status="status" :pending-operation="pending" @operation="pending = $event" />
      <span>Selected: {{ pending ?? 'none' }}</span>
    </div>

    <template #controls>
      <label>
        Status
        <select v-model="status">
          <option>Starting</option>
          <option>Failed</option>
          <option>Restarting</option>
          <option>Running</option>
        </select>
      </label>
      <label>
        Pending operation
        <select v-model="pending">
          <option :value="undefined">None</option>
          <option value="start">Start</option>
          <option value="restart">Restart</option>
          <option value="stop">Stop</option>
        </select>
      </label>
    </template>
  </Story>
</template>

<style scoped>
.preview { display: flex; align-items: center; gap: 16px; min-height: 160px; padding: 24px; }
label { display: block; margin: 12px; }
</style>
