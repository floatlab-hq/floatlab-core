<script setup lang="ts">
import { ref } from 'vue'
import type { Node, LogLine, StorageStats } from '@floatlab/client'
import type { DirectoryApp } from '../../dashboard/types'
import DetailPanel from '../DetailPanel.vue'
import UiTabs from '../UiTabs.vue'
import AppDirectory from './AppDirectory.vue'
import UiNodePlate from './UiNodePlate.vue'
defineProps<{ node: Node; apps: DirectoryApp[]; storage?: StorageStats; logs?: LogLine[]; loading?: boolean }>()
defineEmits<{ close: [] }>()
const tab = ref('overview')
const tabs = ['overview', 'apps', 'storage', 'logs'].map(id => ({ id, label: id[0]!.toUpperCase() + id.slice(1) }))
</script>
<template>
  <DetailPanel :title="node.name" @close="$emit('close')">
    <template #identity><UiNodePlate :name="node.name" /></template>
    <UiTabs v-model="tab" :tabs="tabs" label="Host detail">
      <template #default="{ active }">
        <dl v-if="active === 'overview'">
          <dt>Status</dt><dd>{{ node.status }}</dd><dt>Hostname</dt><dd>{{ node.hostname }}</dd>
          <dt>Role</dt><dd>{{ node.role }}</dd><dt>Addresses</dt><dd>{{ node.addresses.map(address => address.address).join(', ') || '—' }}</dd>
          <dt>OS</dt><dd>{{ node.os_version || '—' }}</dd><dt>Kernel</dt><dd>{{ node.kernel_version || '—' }}</dd>
        </dl>
        <AppDirectory v-else-if="active === 'apps'" :apps="apps" readonly />
        <template v-else-if="active === 'storage'">
          <h2>{{ node.zfs_pool }}</h2>
          <dl v-if="storage"><template v-for="(series, name) in storage.series" :key="name"><dt>{{ name }}</dt><dd>{{ series?.points.at(-1)?.value ?? '—' }}</dd></template></dl>
          <p v-else>{{ loading ? 'Loading storage…' : 'Storage metrics unavailable' }}</p>
        </template>
        <template v-else>
          <pre v-if="logs?.length">{{ logs.map(line => `${line.ts} ${line.stream}: ${line.msg}`).join('\n') }}</pre>
          <p v-else>{{ loading ? 'Loading logs…' : logs ? 'No recent logs' : 'Logs unavailable' }}</p>
        </template>
      </template>
    </UiTabs>
  </DetailPanel>
</template>
<style scoped>
dl { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 12px; overflow-wrap: anywhere; }
dd { margin: 0; } pre { white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
