<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, type DashboardApi, type FloatLabApi, type Node, type Stack, type NodeStats, type StorageStats, type Notification, type RaftHealth, type LogLine, type UpdateApi, type BackupApi, type BackupRestoreApi } from '@floatlab/client'
import { appItem, auditEvent, hostItem, labEvent } from '../../dashboard/live'
import type { AuditEntry } from '@floatlab/client'
import UiButton from '../UiButton.vue'
import DetailPanel from '../DetailPanel.vue'
import CommandConsole from '../CommandConsole.vue'
import LabDashboard from './LabDashboard.vue'
import CreateApp from './CreateApp.vue'
import WorkloadDetail from './WorkloadDetail.vue'
import LabEventLog from './LabEventLog.vue'
import UiNodePlate from './UiNodePlate.vue'
import LiveHostDetail from './LiveHostDetail.vue'
import ApplianceUpdates from './ApplianceUpdates.vue'
import BackupTargets from './BackupTargets.vue'
const props = defineProps<{ api: DashboardApi & FloatLabApi & Partial<UpdateApi & BackupApi & BackupRestoreApi> }>()
const updateApi = computed(() => props.api.listUpdateReleases && props.api.getUpdateStatus && props.api.runUpdateAction ? props.api as UpdateApi : undefined)
const backupApi = computed(() => props.api.listBackupTargets && props.api.putBackupTarget && props.api.removeBackupTarget && props.api.triggerReplication && props.api.listReplication ? props.api as BackupApi & Partial<BackupRestoreApi> : undefined)
const emit = defineEmits<{ logout: [] }>()
const nodes = ref<Node[]>([])
const stacks = ref<Stack[]>([])
const notifications = ref<Notification[]>([])
const activity = ref<AuditEntry[]>([])
const raft = ref<RaftHealth>()
const stats = ref<Record<string, NodeStats>>({})
const storage = ref<Record<string, StorageStats>>({})
const logs = ref<Record<string, LogLine[]>>({})
const loading = ref(false)
const errors = ref<string[]>([])
const selected = ref('')
const selectedApp = ref('')
const creating = ref(false)
const utility = ref('')
const commandConsole = ref<InstanceType<typeof CommandConsole>>()
const hosts = computed(() => nodes.value.map(node => hostItem(node, stacks.value, stats.value[node.id], storage.value[node.id])))
const apps = computed(() => stacks.value.map(stack => appItem(stack, nodes.value)))
const events = computed(() => [
  ...notifications.value.map(notification => ({ ts: notification.created_at, event: labEvent(notification, nodes.value) })),
  ...activity.value.map(entry => ({ ts: entry.ts, event: auditEvent(entry, stacks.value, nodes.value) })),
].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 50).map(value => value.event))
const selectedNode = computed(() => nodes.value.find(node => node.id === selected.value))
const selectedStack = computed(() => stacks.value.find(stack => stack.id === selectedApp.value))
const selectedApps = computed(() => stacks.value.filter(stack => stack.primary_node === selected.value || stack.secondary_node === selected.value).map(stack => appItem(stack, nodes.value)))
const cluster = computed(() => raft.value ? `${raft.value.state} · ${raft.value.peers.length} peers` : 'Cluster status unavailable')
const clusterHealth = computed(() => raft.value ? ['Leader', 'Follower'].includes(raft.value.state) ? 'ready' : 'degraded' : 'unknown')
let active = true
let origin: HTMLElement | undefined
let timer: ReturnType<typeof setInterval> | undefined
async function read<T>(label: string, request: Promise<T>, save: (value: T) => void) {
  try { const value = await request; if (active) save(value) }
  catch (cause) {
    if (!active) return
    if (cause instanceof ApiError && cause.status === 401) { active = false; emit('logout'); return }
    errors.value.push(`${label}: ${cause instanceof Error ? cause.message : 'Unavailable'}`)
  }
}
async function refresh() {
  if (loading.value || !active) return
  loading.value = true; errors.value = []
  stats.value = {}; storage.value = {}; logs.value = {}; raft.value = undefined
  await Promise.all([
    read('Hosts', props.api.listNodes(), value => { nodes.value = value }),
    read('Apps', props.api.listStacks(), value => { stacks.value = value }),
    read('Events', props.api.listNotifications(), value => { notifications.value = value }),
    read('Activity', props.api.listAuditLog(), value => { activity.value = value }),
    read('Cluster', props.api.getRaftHealth(), value => { raft.value = value }),
  ])
  if (!active) return
  await Promise.all(nodes.value.map(node => Promise.all([
    read(`${node.name} metrics`, props.api.getNodeStats(node.id), value => { stats.value[node.id] = value }),
    read(`${node.name} storage`, props.api.getStorageStats(node.id), value => { storage.value[node.id] = value }),
  ])))
  const id = selected.value
  if (active && selectedNode.value) await read('Host logs', props.api.getNodeLogs(id), value => { logs.value[id] = value })
  if (active) loading.value = false
}
async function inspect(id: string) {
  origin = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
  selectedApp.value = ''
  utility.value = ''
  selected.value = id
  await read('Host logs', props.api.getNodeLogs(id), value => { logs.value[id] = value })
}
function inspectApp(id: string) {
  origin = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
  selected.value = ''; selectedApp.value = id; utility.value = ''
}
function action(name: string, id?: string) {
  if (name === 'command') { commandConsole.value?.show(); return }
  if (name === 'settings') { commandConsole.value?.launch(['alert-rules', 'list']); return }
  if (name.startsWith('host-') && id) { void inspect(id); return }
  if (name.startsWith('app-') && id) { inspectApp(id); return }
  if (name === 'add-app') { creating.value = true; return }
  if (name === 'add-host') { commandConsole.value?.launch(['lab', 'invite'], false); return }
  origin = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
  selected.value = ''; selectedApp.value = ''; utility.value = name
}
async function createApp(args: string[]) { creating.value = false; await nextTick(); commandConsole.value?.launch(args) }
async function close() { selected.value = ''; selectedApp.value = ''; utility.value = ''; await nextTick(); origin?.focus({ preventScroll: true }) }
function restoreFocus() { origin?.focus({ preventScroll: true }) }
function visibleRefresh() { if (document.visibilityState === 'visible') void refresh() }
onMounted(() => { void refresh(); timer = setInterval(visibleRefresh, 30_000); document.addEventListener('visibilitychange', visibleRefresh) })
onBeforeUnmount(() => { active = false; clearInterval(timer); document.removeEventListener('visibilitychange', visibleRefresh) })
</script>
<template>
  <main class="live-dashboard">
    <LabDashboard :hosts="hosts" :apps="apps" :events="events.slice(0, 3)" lab-name="My lab" live :loading="loading" :cluster-status="loading ? 'Refreshing…' : cluster" :cluster-health="clusterHealth" :detail-open="Boolean(selectedNode || selectedStack || utility)" @action="action" @closed="restoreFocus">
      <template #command><CommandConsole ref="commandConsole" :api="api" inline @completed="refresh" /></template>
      <template #messages>
        <div v-if="errors.length" role="alert"><p v-for="error in errors" :key="error">{{ error }}</p></div>
        <p v-if="!events.length && !loading">{{ errors.some(error => /^(Events|Activity):/.test(error)) ? 'Events unavailable' : 'No recent events' }}</p>
        <UiButton v-if="events.length > 3" variant="plain" class="event-history" @click="action('events')">View all {{ events.length }} events</UiButton>
      </template>
      <template #hosts-empty>
        <p v-if="!hosts.length && !loading">{{ errors.some(error => error.startsWith('Hosts:')) ? 'Hosts unavailable' : 'No registered hosts' }}</p>
      </template>
      <template #detail>
        <DetailPanel v-if="utility === 'node-key'" title="Node key" @close="close"><dl class="node-key"><template v-for="host in hosts" :key="host.id"><dt><UiNodePlate :name="host.name" /></dt><dd>{{ host.name }}<span>{{ host.address || 'Address unavailable' }}</span></dd></template></dl><p v-if="!hosts.length">No registered hosts</p></DetailPanel>
        <DetailPanel v-if="utility === 'events'" title="Recent lab events" @close="close"><LabEventLog :events="events" readonly /></DetailPanel>
        <WorkloadDetail v-if="selectedStack" :key="selectedStack.id" :stack="selectedStack" :api="api" @close="close" @command="(args, run) => commandConsole?.launch(args, run)" />
        <details v-if="selectedStack && backupApi" class="backup-details"><summary>Backups and restore</summary><BackupTargets :key="selectedStack.id + '-backups'" :api="backupApi" :nodes="nodes" :stacks="[selectedStack]" @restored="refresh" /></details>
        <LiveHostDetail v-if="selectedNode" :key="selectedNode.id" :node="selectedNode" :apps="selectedApps" :storage="storage[selected]" :logs="logs[selected]" :loading="loading" @close="close" />
        <ApplianceUpdates v-if="selectedNode && updateApi" :key="selectedNode.id + '-updates'" :api="updateApi" :node-id="selectedNode.id" />
      </template>
      <template #footer-actions><div class="dashboard-controls"><span v-if="raft?.dev_mode" role="status">Development mode</span><UiButton variant="plain" :disabled="loading" @click="refresh">{{ loading ? 'Refreshing…' : 'Refresh' }}</UiButton><UiButton variant="plain" @click="emit('logout')">Sign out</UiButton></div></template>
    </LabDashboard>
    <CreateApp v-if="creating" :nodes="nodes" @close="creating = false" @create="createApp" />
  </main>
</template>
<style scoped>
.live-dashboard { width: 100%; margin: 0; padding: 0; min-height: 100vh; }
.dashboard-controls { display: flex; align-items: center; gap: 16px; margin-left: auto; flex-wrap: wrap; }
.event-history { display: block; margin: 8px 0 0 auto; }
.node-key { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 16px; }
.node-key dd { margin: 0; overflow-wrap: anywhere; }
.node-key dd span { display: block; margin-top: 4px; color: var(--text-placeholder); font-size: var(--text-meta); }
.backup-details { padding: 16px; background: var(--surface-recessed); }
.backup-details summary { cursor: pointer; }
[role="alert"] { color: var(--status-error); }
</style>
