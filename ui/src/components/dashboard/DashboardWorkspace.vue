<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { HostItem, DirectoryApp, LabEvent } from '../../dashboard/types'
import type { FormValues } from '../../forms/schema'
import { coreForms } from '../../forms'
import SplitView from '../SplitView.vue'
import DetailPanel from '../DetailPanel.vue'
import ApiForm from '../ApiForm.vue'
import CommandPalette from '../CommandPalette.vue'
import DashboardNavigation from './DashboardNavigation.vue'
import LabDashboard from './LabDashboard.vue'
import HostDetail from './HostDetail.vue'
const props = withDefaults(defineProps<{ hosts: HostItem[]; apps: DirectoryApp[]; events: LabEvent[]; initialPanel?: string }>(), { initialPanel: '' })
const emit = defineEmits<{ action: [action: string, id?: string]; submit: [screen: string, values: Record<string, unknown>] }>()
const panel = ref(props.initialPanel)
const selectedId = ref(props.hosts[0]?.id ?? '')
const selectedHost = computed(() => props.hosts.find(host => host.id === selectedId.value))
const draft = ref<FormValues>({})
const commandOpen = ref(false)
let origin: HTMLElement | null = null
const commands = [{ id: 'add-host', label: 'Add host', shortcut: 'H' }, { id: 'add-app', label: 'Add app', shortcut: 'A' }, { id: 'settings', label: 'Settings', shortcut: 'S' }]
const forms = computed(() => panel.value === 'add-host' ? coreForms.NodeCreate : panel.value === 'add-app' ? coreForms.StackCreate : coreForms.AlertRuleCreate)
const title = computed(() => panel.value === 'add-host' ? 'Add host' : panel.value === 'add-app' ? 'Add app' : 'Alert settings')
function action(name: string, id?: string) {
  emit('action', name, id)
  if (['host-inspect', 'host-tools'].includes(name)) { selectedId.value = id ?? ''; panel.value = 'host' }
  else if (['settings', 'add-host', 'add-app'].includes(name)) { panel.value = name; draft.value = {} }
  else return
  origin = document.activeElement instanceof HTMLElement ? document.activeElement : null
}
async function close() { panel.value = ''; await nextTick(); origin?.focus({ preventScroll: true }) }
function restoreFocus() { origin?.focus({ preventScroll: true }) }
</script>
<template>
  <div class="dashboard-workspace">
    <DashboardNavigation @command="commandOpen = true" @settings="action('settings')" />
    <SplitView :open="Boolean(panel)" @closed="restoreFocus">
      <LabDashboard :hosts="hosts" :apps="apps" :events="events" embedded @action="action" />
      <template #detail>
        <HostDetail v-if="panel === 'host' && selectedHost" :key="selectedHost.id" :host="selectedHost" :apps="apps" @close="close" @action="action" @save="emit('submit', 'host', $event)" />
        <DetailPanel v-else :key="panel" :title="title" @close="close"><ApiForm v-model="draft" :schema="forms" :submit-label="panel === 'settings' ? 'Save alert rule' : 'Create'" @submit="emit('submit', panel, $event)" /></DetailPanel>
      </template>
    </SplitView>
    <CommandPalette :open="commandOpen" :commands="commands" @close="commandOpen = false" @select="action($event)" />
  </div>
</template>
<style scoped>
.dashboard-workspace { display: grid; grid-template-columns: minmax(0, 1fr); gap: 26px; padding: 32px 48px; background: var(--surface-canvas); color: var(--text-secondary); font-family: var(--font-mono); font-weight: var(--weight-ui); min-height: 1060px; }
@media (max-width: 900px) { .dashboard-workspace { padding: 16px; } }
</style>
