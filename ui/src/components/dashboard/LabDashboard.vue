<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Container } from '@floatlab/client'
import { IconPlusFilled, IconFilterFilled } from '@tabler/icons-vue'
import type { HostItem, DirectoryApp, LabEvent } from '../../dashboard/types'
import UiButton from '../UiButton.vue'
import CommandPalette from '../CommandPalette.vue'
import SplitView from '../SplitView.vue'
import DashboardNavigation from './DashboardNavigation.vue'
import LabEventLog from './LabEventLog.vue'
import HostDirectory from './HostDirectory.vue'
import AppDirectory from './AppDirectory.vue'
import UiHealthMark from './UiHealthMark.vue'

const props = withDefaults(defineProps<{
  hosts: HostItem[]
  apps: DirectoryApp[]
  events: LabEvent[]
  embedded?: boolean
  labName?: string
  filterCount?: number
  hiddenAppCount?: number
  live?: boolean
  loading?: boolean
  eventsOpen?: boolean
  loadContainers?: (id: string) => Promise<Container[]>
  detailOpen?: boolean
}>(), { loadContainers: undefined, labName: 'Cavs lab', filterCount: 3, hiddenAppCount: 32 })

const emit = defineEmits<{ action: [action: string, id?: string]; closed: [] }>()
const errorCount = computed(() => props.events.filter(event => event.tone === 'error').length)
const warningCount = computed(() => props.events.filter(event => event.tone === 'warning').length)
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
      <DashboardNavigation v-if="!embedded" :error-count="errorCount" :warning-count="warningCount" :events-open="eventsOpen" @events="emit('action', 'events')" @command="live ? emit('action', 'command') : commandOpen = true" @settings="emit('action', 'settings')" @node-key="emit('action', 'node-key')" @logout="emit('action', 'logout')"><template v-if="$slots.command" #command><slot name="command" /></template></DashboardNavigation>
      <LabEventLog v-if="!live" :events="events" :readonly="live" @select="emit('action', 'event', $event)" />
      <slot name="messages" />
    </div>
    <SplitView :open="detailOpen" @closed="emit('closed')">
      <div class="dashboard-directories">
        <section aria-labelledby="lab-heading">
          <div class="dashboard-section-heading">
            <h1 id="lab-heading">{{ labName }}</h1>
            <UiButton variant="surface" class="section-action" @click="emit('action', 'add-host')"><IconPlusFilled :size="18" aria-hidden="true" /> HOST</UiButton>
          </div>
          <HostDirectory :hosts="hosts" @tools="emit('action', 'host-tools', $event)" @inspect="emit('action', 'host-inspect', $event)" @connect="(id, target) => emit('action', `host-${target}`, id)" />
          <slot name="hosts-empty" />
        </section>
        <section aria-labelledby="apps-heading">
          <div class="dashboard-section-heading">
            <h2 id="apps-heading">Apps</h2>
            <UiButton v-if="!live" variant="surface" class="section-action" aria-label="Filter apps" @click="emit('action', 'filters')"><IconFilterFilled :size="18" aria-hidden="true" /></UiButton>
            <UiButton variant="surface" :class="{ 'section-action': live }" :disabled="live && (!hosts.length || loading)" @click="emit('action', 'add-app')"><IconPlusFilled :size="18" aria-hidden="true" /> App</UiButton>
          </div>
          <AppDirectory :apps="apps" :managed="live" :load-containers="loadContainers" @command="(id, action) => emit('action', `app-${action}`, id)" @open="emit('action', 'app-open', $event)" />
        </section>
      </div>
      <template #detail><slot name="detail" /></template>
    </SplitView>
    <footer class="dashboard-footer">
      <span class="dashboard-legend"><UiHealthMark state="ready" /> Ready <UiHealthMark state="stopped" /> Stopped <span aria-hidden="true">!</span> Backup attention</span>
      <slot name="footer-actions" />
    </footer>
    <p v-if="filterCount && !live" class="dashboard-filter-note">{{ filterCount }} filters applied. <UiButton variant="plain" @click="emit('action', 'clear-filters')">× Remove filters</UiButton> to view {{ hiddenAppCount }} other apps.</p>
    <CommandPalette v-if="!embedded && !live" :open="commandOpen" :commands="commands" @close="commandOpen = false" @select="emit('action', $event)" />
  </div>
</template>

<style scoped>
.lab-dashboard { display: flex; flex-direction: column; gap: var(--space-section); width: 100%; min-height: 1060px; padding: 32px 48px; background: var(--surface-canvas); color: var(--text-secondary); font-family: var(--font-mono); font-weight: var(--weight-ui); }
.lab-dashboard { container-type: inline-size; }
.lab-dashboard.embedded { padding: 0; min-height: 0; }
.dashboard-heading-area { position: relative; }
.dashboard-directories { display: grid; gap: var(--space-section); }
.dashboard-section-heading { display: flex; align-items: center; gap: 18px; min-height: var(--height-control); }
.dashboard-section-heading h1, .dashboard-section-heading h2 { margin: 0; color: var(--text-primary); font-family: var(--font-mono); font-size: var(--text-heading); font-weight: var(--weight-ui); line-height: var(--line-heading); }
.section-action { margin-left: auto; }
.dashboard-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: var(--text-control); }
.dashboard-footer > .button { display: inline-flex; align-items: center; gap: var(--space-1); }
.dashboard-legend { display: inline-flex; align-items: center; gap: var(--space-2); font-size: var(--text-meta); line-height: 15px; }
.dashboard-filter-note { margin: 0; color: var(--text-secondary); font-size: var(--text-meta); line-height: 15px; text-align: center; }
.dashboard-filter-note .button { text-decoration: underline; }
@media (max-width: 800px) { .lab-dashboard { padding: var(--space-5); } }
@container (max-width: 700px) { .dashboard-section-heading { gap: 8px; } .dashboard-footer { flex-wrap: wrap; } .dashboard-legend { flex-wrap: wrap; } }
</style>
