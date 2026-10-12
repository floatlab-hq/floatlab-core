<script setup lang="ts">
import { ref } from 'vue'
import type { HostItem, DirectoryApp } from '../../dashboard/types'
import { coreForms, hostPreferences } from '../../forms'
import type { FormValues } from '../../forms/schema'
import DetailPanel from '../DetailPanel.vue'
import UiTabs from '../UiTabs.vue'
import UiButton from '../UiButton.vue'
import ApiForm from '../ApiForm.vue'
import UiNodePlate from './UiNodePlate.vue'
import UiHealthMark from './UiHealthMark.vue'
import AppDirectory from './AppDirectory.vue'
const props = defineProps<{ host: HostItem; apps: DirectoryApp[] }>()
defineEmits<{ close: []; action: [action: string]; save: [values: Record<string, unknown>] }>()
const tab = ref('overview')
const values = ref<FormValues>({ name: props.host.name, hostname: `${props.host.name}.local`, role: 'secondary', zfs_pool: 'floatlab' })
const preferences = ref<FormValues>({ resource_alerts: true, automatic_updates: false })
const tabs = [{ id: 'overview', label: 'Overview' }, { id: 'apps', label: 'Apps' }, { id: 'storage', label: 'Storage' }, { id: 'logs', label: 'Logs' }, { id: 'config', label: 'Config' }]
</script>
<template>
  <DetailPanel :title="String(values.name || host.name)" @close="$emit('close')">
    <template #identity><UiNodePlate :name="String(values.name || host.name)" /></template>
    <div class="host-detail-actions"><h2>Host</h2><UiButton v-for="action in ['Pause', 'Restart', 'Shut down', 'More actions']" :key="action" variant="surface" @click="$emit('action', action)">{{ action }}</UiButton></div>
    <p class="host-detail-facts"><UiHealthMark :state="host.health" /> {{ host.health === 'ready' ? 'Online' : host.health }} · {{ values.hostname }} · checked just now</p>
    <UiTabs v-model="tab" :tabs="tabs" label="Host detail">
      <template #default="{ active }">
        <template v-if="active === 'overview' || active === 'config'">
          <h3>Host settings</h3>
          <ApiForm v-model="values" :schema="coreForms.NodeUpdate" @submit="$emit('save', $event)" />
          <template v-if="active === 'overview'"><h3>Host preferences <small>Design preview</small></h3><ApiForm v-model="preferences" :schema="hostPreferences" submit-label="Preview preferences" /></template>
        </template>
        <template v-else-if="active === 'apps'"><h3>Apps on this host</h3><AppDirectory :apps="apps.filter(app => app.host === host.name)" @open="$emit('action', 'Open app')" /></template>
        <template v-else-if="active === 'storage'"><h3>Storage</h3><dl><dt>ZFS pool</dt><dd>{{ values.zfs_pool }}</dd></dl></template>
        <template v-else><h3>Logs</h3><p class="empty-detail">No log stream connected in this local preview.</p></template>
      </template>
    </UiTabs>
  </DetailPanel>
</template>
<style scoped>
.host-detail-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
h2 { margin: 0 auto 0 0; font-size: var(--text-heading); font-weight: var(--weight-ui); }
h3 { margin: 0 0 16px; font-size: var(--text-subheading); line-height: var(--line-subheading); font-weight: var(--weight-ui); }
h3:not(:first-child) { margin-top: 18px; }
small { color: var(--text-placeholder); font-size: var(--text-meta); }
.host-detail-facts { display: flex; align-items: center; gap: 8px; margin: 18px 0; font-size: var(--text-meta); line-height: var(--line-meta); overflow-wrap: anywhere; }
dl { display: flex; gap: 16px; font-size: var(--text-status); }
.empty-detail { font-size: var(--text-status); }
</style>
