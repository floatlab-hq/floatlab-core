<script setup lang="ts">
import { IconArrowUpRight, IconShieldCheckFilled, IconChevronDownFilled, IconColumns2Filled } from '@tabler/icons-vue'
import type { DirectoryApp } from '../../dashboard/types'
import UiButton from '../UiButton.vue'
import UiNodePlate from './UiNodePlate.vue'
import UiStatusBadge from './UiStatusBadge.vue'
import UiAssetIcon from './UiAssetIcon.vue'

defineProps<{ apps: DirectoryApp[]; readonly?: boolean }>()
defineEmits<{ state: [appId: string]; tools: [appId: string]; open: [appId: string]; network: [appId: string]; columns: [] }>()
</script>

<template>
  <div class="directory-scroll">
    <div class="app-directory" role="table" aria-label="Apps">
      <div class="app-columns" role="row">
        <span v-for="label in ['App', 'State', 'Network', 'Host', 'HA standby', 'Backup node', 'Security', 'Group']" :key="label" role="columnheader">{{ label }}</span>
        <span role="columnheader"><UiButton v-if="!readonly" variant="plain" aria-label="Choose app columns" @click="$emit('columns')"><IconColumns2Filled :size="18" aria-hidden="true" /></UiButton></span>
      </div>
      <div v-for="app in apps" :key="app.id" class="app-row" role="row">
        <div class="app-identity" role="cell">
          <UiAssetIcon v-if="app.logo" :src="app.logo" />
          <span>{{ app.name }}</span>
          <UiButton v-if="!readonly" variant="plain" class="app-open" :aria-label="`Open ${app.name}`" @click="$emit('open', app.id)"><IconArrowUpRight :size="18" aria-hidden="true" /></UiButton>
        </div>
        <span role="cell"><UiStatusBadge :disabled="readonly" :state="app.state" @click="$emit('state', app.id)" /></span>
        <span role="cell"><span v-if="readonly">{{ app.address }}</span><UiButton v-else variant="plain" class="directory-link" @click="$emit('network', app.id)">{{ app.address }}</UiButton></span>
        <span role="cell"><UiNodePlate v-if="app.host" :name="app.host" /><span v-else>—</span></span>
        <span role="cell"><UiNodePlate v-if="app.standby" :name="app.standby" /><span v-else>—</span></span>
        <span role="cell"><UiNodePlate v-if="app.backup" :name="app.backup" /><span v-else>—</span></span>
        <span class="app-security" role="cell"><template v-if="app.tls"><IconShieldCheckFilled :size="20" aria-hidden="true" /> TLS</template><span v-else>—</span></span>
        <span class="app-group" role="cell">{{ app.group }}</span>
        <span role="cell"><UiButton v-if="!readonly" variant="plain" class="app-tools" :aria-label="`Tools for ${app.name}`" @click="$emit('tools', app.id)">Tools <IconChevronDownFilled :size="12" aria-hidden="true" /></UiButton></span>
      </div>
      <p v-if="!apps.length" class="directory-empty">{{ readonly ? 'No apps' : 'No apps match these filters.' }}</p>
    </div>
  </div>
</template>

<style scoped>
.directory-scroll { overflow-x: auto; container-type: inline-size; }
.app-directory { min-width: 1300px; }
.app-columns, .app-row { display: grid; grid-template-columns: minmax(300px, 1fr) 90px 170px repeat(3, 136px) 120px 104px 90px; align-items: center; }
.app-columns { height: 42px; color: var(--text-secondary); font-size: var(--text-column); line-height: var(--line-column); letter-spacing: var(--tracking-column); text-transform: uppercase; }
.app-columns > :first-child { padding-left: 12px; }
.app-columns > :last-child { text-align: right; padding-right: 5px; }
.app-row { height: var(--height-app-row); margin-bottom: 2px; padding-left: var(--space-2); border-radius: var(--radius); background: var(--surface-default); color: var(--text-secondary); font-size: var(--text-status); line-height: var(--line-body); }
.app-row > [role="cell"]:nth-child(n+3) { padding: 0 var(--space-3); }
.app-identity { display: flex; align-items: center; gap: var(--space-3); min-width: 0; padding-left: var(--space-3); color: var(--text-primary); font-size: var(--text-item); }
.app-identity > span:not(.asset-icon) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.app-open { color: var(--omarchy-accent); }
.app-security { display: flex; align-items: center; gap: var(--space-2); }
.app-security svg { color: var(--status-success); }
.app-group { letter-spacing: 1px; }
.app-tools { display: inline-flex; align-items: center; gap: var(--space-1); font-size: var(--text-control); }
.directory-link { font-size: var(--text-status); text-decoration: underline; }
.directory-empty { padding: var(--space-5); text-align: center; }

@container (max-width: 700px) {
.app-directory { min-width: 0; }
.app-columns, .app-row { grid-template-columns: minmax(0,1fr) 85px 70px 28px; }
.app-columns > :nth-child(3), .app-row > :nth-child(3), .app-columns > :nth-child(n+5):not(:last-child), .app-row > :nth-child(n+5):not(:last-child) { display: none; }
.app-row { padding-left: 0; }
.app-identity { gap: 8px; padding-left: 8px; font-size: var(--text-status); }
.app-identity :deep(.asset-icon) { flex-shrink: 0; }
.app-row > [role="cell"]:nth-child(n+3) { padding: 0; }
.app-tools { font-size: 0; }
.app-open { display: none; }
}
</style>
