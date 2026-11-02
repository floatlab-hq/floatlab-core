<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { IconPlusFilled } from '@tabler/icons-vue'
import { ApiError, type DashboardApi, type FloatLabApi, type Node, type Stack, type NodeStats, type StorageStats, type Notification, type RaftHealth, type LogLine, type UpdateApi, type BackupApi, type BackupRestoreApi } from '@floatlab/client'
import { appItem, auditEvent, hostItem, labEvent } from '../../dashboard/live'
import type { AuditEntry } from '@floatlab/client'
import UiButton from '../UiButton.vue'
import SplitView from '../SplitView.vue'
import CommandConsole from '../CommandConsole.vue'
import DashboardNavigation from './DashboardNavigation.vue'
import CreateApp from './CreateApp.vue'
import WorkloadDetail from './WorkloadDetail.vue'
import HostDirectory from './HostDirectory.vue'
import AppDirectory from './AppDirectory.vue'
import LabEventLog from './LabEventLog.vue'
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
  selected.value = id
  await read('Host logs', props.api.getNodeLogs(id), value => { logs.value[id] = value })
}
function inspectApp(id: string) {
  origin = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
  selected.value = ''; selectedApp.value = id
}
async function createApp(args: string[]) { creating.value = false; await nextTick(); commandConsole.value?.launch(args) }
async function close() { selected.value = ''; selectedApp.value = ''; await nextTick(); origin?.focus({ preventScroll: true }) }
function restoreFocus() { origin?.focus({ preventScroll: true }) }
function visibleRefresh() { if (document.visibilityState === 'visible') void refresh() }
onMounted(() => { void refresh(); timer = setInterval(visibleRefresh, 30_000); document.addEventListener('visibilitychange', visibleRefresh) })
onBeforeUnmount(() => { active = false; clearInterval(timer); document.removeEventListener('visibilitychange', visibleRefresh) })
</script>
<template>
  <main id="dashboard" class="live-dashboard">
    <DashboardNavigation @command="commandConsole?.show()" @settings="commandConsole?.launch(['alert-rules', 'list'])" />
    <div class="dashboard-toolbar">
      <p v-if="raft?.dev_mode" role="status">Development mode</p>
      <p role="status">{{ loading ? 'Refreshing…' : cluster }}</p>
      <UiButton variant="surface" :disabled="loading" @click="refresh">Refresh</UiButton>
      <UiButton variant="surface" @click="emit('logout')">Sign out</UiButton>
    </div>
    <div v-if="errors.length" role="alert"><p v-for="error in errors" :key="error">{{ error }}</p></div>
    <LabEventLog :events="events" readonly />
    <p v-if="!events.length && !loading">{{ errors.some(error => /^(Events|Activity):/.test(error)) ? 'Events unavailable' : 'No recent events' }}</p>
    <SplitView :open="Boolean(selectedNode || selectedStack)" @closed="restoreFocus">
      <div>
        <div class="dashboard-section-heading"><h1>Hosts</h1><UiButton variant="surface" @click="commandConsole?.launch(['lab', 'invite'], false)"><IconPlusFilled :size="18" aria-hidden="true" /> Host</UiButton></div>
        <HostDirectory :hosts="hosts" @inspect="inspect" @tools="inspect" @connect="inspect" />
        <p v-if="!hosts.length && !loading">{{ errors.some(error => error.startsWith('Hosts:')) ? 'Hosts unavailable' : 'No registered hosts' }}</p>
        <div class="dashboard-section-heading"><h2>Apps</h2><UiButton variant="surface" :disabled="!nodes.length" @click="creating = true"><IconPlusFilled :size="18" aria-hidden="true" /> App</UiButton></div>
        <AppDirectory :apps="apps" managed @state="inspectApp" @tools="inspectApp" @open="inspectApp" />
      </div>
      <template #detail>
        <WorkloadDetail v-if="selectedStack" :key="selectedStack.id" :stack="selectedStack" :api="api" @close="close" @command="(args, run) => commandConsole?.launch(args, run)" />
        <LiveHostDetail v-if="selectedNode" :key="selectedNode.id" :node="selectedNode" :apps="selectedApps" :storage="storage[selected]" :logs="logs[selected]" :loading="loading" @close="close" />
        <ApplianceUpdates v-if="selectedNode && updateApi" :key="selectedNode.id + '-updates'" :api="updateApi" :node-id="selectedNode.id" />
      </template>
    </SplitView>
    <BackupTargets v-if="backupApi && stacks.length" :api="backupApi" :nodes="nodes" :stacks="stacks" @restored="refresh" />
    <CreateApp v-if="creating" :nodes="nodes" @close="creating = false" @create="createApp" />
    <CommandConsole ref="commandConsole" :api="api" @completed="refresh" />
  </main>
</template>
<style scoped>
.live-dashboard { display: grid; gap: 26px; padding: 32px 48px; }
.dashboard-toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.dashboard-toolbar p:first-child { margin-right: auto; }
.dashboard-section-heading { display: flex; align-items: center; justify-content: space-between; gap: 18px; margin: 24px 0 12px; }
.dashboard-section-heading h1, .dashboard-section-heading h2 { margin: 0; font-size: var(--text-heading); font-weight: var(--weight-ui); }
[role="alert"] { color: var(--status-error); } @media (max-width: 900px) { .live-dashboard { padding: 16px; } }
</style>
