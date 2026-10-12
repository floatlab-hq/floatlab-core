<script setup lang="ts">
import type { AppRecord, Operation } from '../types'
import ResourceBar from './ResourceBar.vue'
import ResourceTrack from './ResourceTrack.vue'
import StatusActionMenu from './StatusActionMenu.vue'
import UiButton from './UiButton.vue'

defineProps<{ app: AppRecord }>()
const emit = defineEmits<{ operation: [operation: Operation] }>()
</script>

<template>
  <tr>
    <td><span class="app-icon" :class="app.iconClass">{{ app.icon }}</span>{{ app.name }}</td>
    <td>
      <StatusActionMenu
        :status="app.status"
        :pending-operation="app.pendingOperation"
        @operation="emit('operation', $event)"
      />
    </td>
    <td>
      <div class="links">
        <a v-for="link in app.links" :key="link" :href="link">{{ link }}</a>
        <span v-if="!app.links">{{ app.address }}</span>
      </div>
      <UiButton v-if="app.notice && app.noticeTone === 'error'" class="inline-alert">
        {{ app.notice }}
      </UiButton>
      <div v-else-if="app.notice && app.noticeTone === 'warning'" class="warning">{{ app.notice }}</div>
      <div v-else-if="app.notice">{{ app.notice }}</div>
    </td>
    <td><ResourceBar :value="app.cpu" /></td>
    <td><ResourceBar :value="app.memory" /></td>
    <td><ResourceBar :value="app.storage" /></td>
    <td><ResourceBar :value="app.disk" /></td>
    <td>
      <div class="cell-metric network-cell">
        <div class="row-network-values">
          <span>↑ {{ app.network.up.label }}</span>
          <span>↓ {{ app.network.down.label }}</span>
        </div>
        <div class="row-network-bars">
          <ResourceTrack :percent="app.network.up.percent" reverse />
          <ResourceTrack :percent="app.network.down.percent" />
        </div>
      </div>
    </td>
  </tr>
</template>
