<script setup lang="ts">
import { ref } from 'vue'
import { IconPlusFilled, IconFilterFilled, IconArrowUpRight } from '@tabler/icons-vue'
import type { HostItem, DirectoryApp, LabEvent } from '../../dashboard/types'
import UiButton from '../UiButton.vue'
import CommandPalette from '../CommandPalette.vue'
import DashboardNavigation from './DashboardNavigation.vue'
import LabEventLog from './LabEventLog.vue'
import HostDirectory from './HostDirectory.vue'
import AppDirectory from './AppDirectory.vue'
import UiHealthMark from './UiHealthMark.vue'

withDefaults(defineProps<{
  hosts: HostItem[]
  apps: DirectoryApp[]
  events: LabEvent[]
  embedded?: boolean
  labName?: string
  filterCount?: number
  hiddenAppCount?: number
}>(), { labName: 'Cavs lab', filterCount: 3, hiddenAppCount: 32 })

const emit = defineEmits<{ action: [action: string, id?: string] }>()
const commandOpen = ref(false)
const commands = [
  { id: 'add-app', label: 'Add app', shortcut: 'A' },
  { id: 'add-host', label: 'Add host', shortcut: 'H' },
  { id: 'settings', label: 'Settings', shortcut: 'S' },
]
</script>

<template>
  <div id="dashboard" class="lab-dashboard" :class="{ embedded }">
    <div class="dashboard-heading-area">
      <DashboardNavigation v-if="!embedded" @command="commandOpen = true" @settings="emit('action', 'settings')" />
      <LabEventLog :events="events" @select="emit('action', 'event', $event)" />
    </div>
    <section aria-labelledby="lab-heading">
      <div class="dashboard-section-heading">
        <h1 id="lab-heading">{{ labName }}</h1>
        <span class="dashboard-quorum"><UiHealthMark state="ready" /> quorum healthy</span>
        <UiButton variant="surface" class="section-action" @click="emit('action', 'add-host')"><IconPlusFilled :size="18" aria-hidden="true" /> HOST</UiButton>
      </div>
      <HostDirectory :hosts="hosts" @tools="emit('action', 'host-tools', $event)" @inspect="emit('action', 'host-inspect', $event)" @connect="(id, target) => emit('action', `host-${target}`, id)" />
    </section>
    <section aria-labelledby="apps-heading">
      <div class="dashboard-section-heading">
        <h2 id="apps-heading">Apps</h2>
        <UiButton variant="surface" class="section-action" aria-label="Filter apps" @click="emit('action', 'filters')"><IconFilterFilled :size="18" aria-hidden="true" /></UiButton>
        <UiButton variant="surface" @click="emit('action', 'add-app')"><IconPlusFilled :size="18" aria-hidden="true" /> App</UiButton>
      </div>
      <AppDirectory :apps="apps" @state="emit('action', 'app-state', $event)" @tools="emit('action', 'app-tools', $event)" @open="emit('action', 'app-open', $event)" @network="emit('action', 'app-network', $event)" @columns="emit('action', 'columns')" />
    </section>
    <footer class="dashboard-footer">
      <span class="dashboard-legend"><UiHealthMark state="ready" /> Ready <UiHealthMark state="stopped" /> Stopped <span aria-hidden="true">!</span> Backup attention</span>
      <UiButton variant="plain" @click="emit('action', 'node-key')">Node key <IconArrowUpRight :size="13" aria-hidden="true" /></UiButton>
    </footer>
    <p v-if="filterCount" class="dashboard-filter-note">{{ filterCount }} filters applied. <UiButton variant="plain" @click="emit('action', 'clear-filters')">× Remove filters</UiButton> to view {{ hiddenAppCount }} other apps.</p>
    <CommandPalette v-if="!embedded" :open="commandOpen" :commands="commands" @close="commandOpen = false" @select="emit('action', $event)" />
  </div>
</template>

<style scoped>
.lab-dashboard { display: flex; flex-direction: column; gap: var(--space-section); width: 100%; min-height: 1060px; padding: 32px 48px; background: var(--surface-canvas); color: var(--text-secondary); font-family: var(--font-mono); font-weight: var(--weight-ui); }
.lab-dashboard { container-type: inline-size; }
.lab-dashboard.embedded { padding: 0; min-height: 0; }
.dashboard-heading-area { position: relative; }
.dashboard-section-heading { display: flex; align-items: center; gap: 18px; min-height: var(--height-control); }
.dashboard-section-heading h1, .dashboard-section-heading h2 { margin: 0; color: var(--text-primary); font-family: var(--font-mono); font-size: var(--text-heading); font-weight: var(--weight-ui); line-height: var(--line-heading); }
.dashboard-quorum { display: inline-flex; align-items: center; gap: 6px; font-size: var(--text-control); }
.section-action { margin-left: auto; }
.dashboard-footer { display: flex; align-items: center; justify-content: space-between; font-size: var(--text-control); }
.dashboard-footer > .button { display: inline-flex; align-items: center; gap: var(--space-1); }
.dashboard-legend { display: inline-flex; align-items: center; gap: var(--space-2); font-size: var(--text-meta); line-height: 15px; }
.dashboard-filter-note { margin: 0; color: var(--text-secondary); font-size: var(--text-meta); line-height: 15px; text-align: center; }
.dashboard-filter-note .button { text-decoration: underline; }
@media (max-width: 800px) { .lab-dashboard { padding: var(--space-5); } .dashboard-quorum { display: none; } }
@container (max-width: 700px) { .dashboard-quorum { display: none; } .dashboard-section-heading { gap: 8px; } .dashboard-footer { flex-wrap: wrap; } .dashboard-legend { flex-wrap: wrap; } }
</style>
