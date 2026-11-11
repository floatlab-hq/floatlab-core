<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, CommandEngine, idempotencyKey, type DashboardApi, type FloatLabApi, type Node, type Stack, type NodeStats, type StorageStats, type Notification, type RaftHealth, type LogLine, type UpdateApi, type BackupApi, type BackupRestoreApi, type BackupTarget } from '@floatlab/client'
import type { StackMoveApi, Operation, HostNetworkApi, NetworkPool, HttpApi } from '@floatlab/client'
import { appItem, auditEvent, hostItem, labEvent } from '../../dashboard/live'
import { protectionStatus } from '../../dashboard/protection'
import UiPlateCard from '../UiPlateCard.vue'
import UiButton from '../UiButton.vue'
import UiSelect from '../UiSelect.vue'
import AppPlateActions from './AppPlateActions.vue'
import type { AuditEntry } from '@floatlab/client'
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
const props = defineProps<{ api: DashboardApi & FloatLabApi & Partial<UpdateApi & BackupApi & BackupRestoreApi & StackMoveApi & HttpApi & Pick<HostNetworkApi, 'listNetworkPools'>> }>()
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
const protection = ref<Record<string, { targets: BackupTarget[]; jobs: Record<string, unknown>[]; error?: string; unavailable?: boolean }>>({})
const pendingActions = ref(new Set<string>())
type MoveProgress = { name: string; host: string; published: boolean; address?: string; operationId?: string; state: Operation['state']; checkpoint?: string; error?: string }
const moves = ref<Record<string, MoveProgress>>({})
const pollingMoves = new Set<string>()
const moveDestination = ref('')
const movePool = ref('')
const movePools = ref<NetworkPool[]>([])
const moveSubmitError = ref('')
const moveRequestKeys = new Map<string, { request: string; key: string }>()
const moveHosts = (stack?: Stack) => stack ? nodes.value.filter(node => node.id !== stack.primary_node && node.status !== 'offline') : []
const canMove = (stack: Stack) => Boolean(props.api.moveStack && props.api.getOperation && stack.state === 'RunningPrimary' && moveHosts(stack).length)
const movePending = (id: string) => ['pending', 'running'].includes(moves.value[id]?.state ?? '')
const moveCheckpoint: Record<string, string> = {
  seeding: 'Replicating while the app runs',
  'reserving-destination': 'Reserving resources on the destination host',
  'stopping-source': 'Stopping the app on the source host',
  'final-sync': 'Sending the final changes',
  'transferring-ownership': 'Transferring ownership',
  'starting-destination': 'Starting the app on the destination host',
}
function moveMessage(move: MoveProgress) {
  if (move.state === 'succeeded') return `${move.name} moved to ${move.host}${move.address ? ` · New IP address ${move.address}` : ''}`
  if (move.state === 'failed') return `Move of ${move.name} to ${move.host} failed`
  return `Moving ${move.name} to ${move.host} · ${moveCheckpoint[move.checkpoint ?? ''] ?? 'Preparing the move'}`
}
const restartMotion = ref<Record<string, 'restart'>>({})
const restartTimers = new Map<string, ReturnType<typeof setTimeout>>()
function acceptedMutation(path: string) {
  const match = /^\/stacks\/([^/]+)\/restart$/.exec(path)
  if (!active || !match) return
  const id = decodeURIComponent(match[1]!)
  clearTimeout(restartTimers.get(id))
  restartMotion.value[id] = 'restart'
  restartTimers.set(id, setTimeout(() => { delete restartMotion.value[id]; restartTimers.delete(id) }, 3200))
}
const apps = computed(() => stacks.value.map(stack => ({ ...appItem(stack, nodes.value), actionPending: pendingActions.value.has(stack.id) || movePending(stack.id), moveAvailable: canMove(stack), runtimeMotion: restartMotion.value[stack.id], protectionAvailable: Boolean(backupApi.value) && !protection.value[stack.id]?.unavailable, ...protectionStatus(stack, nodes.value, protection.value[stack.id]?.targets ?? [], protection.value[stack.id]?.jobs ?? [], protection.value[stack.id]?.error) })))
const events = computed(() => [
  ...notifications.value.map(notification => ({ ts: notification.created_at, event: labEvent(notification, nodes.value) })),
  ...activity.value.map(entry => ({ ts: entry.ts, event: auditEvent(entry, stacks.value, nodes.value) })),
].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 50).map(value => value.event))
const selectedNode = computed(() => nodes.value.find(node => node.id === selected.value))
const selectedStack = computed(() => stacks.value.find(stack => stack.id === selectedApp.value))
const selectedApps = computed(() => apps.value.filter(app => stacks.value.some(stack => stack.id === app.id && (stack.primary_node === selected.value || stack.secondary_node === selected.value))))
const selectedItem = computed(() => apps.value.find(app => app.id === selectedApp.value))
const selectedMove = computed(() => moves.value[selectedApp.value])
const eligibleMovePools = computed(() => movePools.value.filter(pool => pool.membership_resolved && pool.node_ids.includes(moveDestination.value)))
let active = true
let origin: HTMLElement | undefined
let timer: ReturnType<typeof setInterval> | undefined
let protectionTimer: ReturnType<typeof setInterval> | undefined
let moveTimer: ReturnType<typeof setInterval> | undefined
let protectionGeneration = 0
let protectionLoading = false
async function refreshProtection() {
  if (!active || protectionLoading || !backupApi.value) return
  protectionLoading = true
  const generation = ++protectionGeneration
  const api = backupApi.value
  await Promise.all(stacks.value.map(async stack => {
    try {
      const [targets, jobs] = await Promise.all([api.listBackupTargets(stack.id), api.listReplication(stack.id)])
      if (active && generation === protectionGeneration) protection.value[stack.id] = { targets, jobs }
    } catch (cause) {
      if (!active || generation !== protectionGeneration) return
      if (cause instanceof ApiError && cause.status === 401) { active = false; emit('logout'); return }
      protection.value[stack.id] = { targets: [], jobs: [], error: cause instanceof Error ? cause.message : 'Protection status unavailable', unavailable: cause instanceof ApiError && [404, 501].includes(cause.status) }
    }
  }))
  protectionLoading = false
}
function refreshActiveJobs() {
  if (document.visibilityState !== 'visible') return
  if (stacks.value.some(stack => !['RunningPrimary', 'RunningBackup', 'Idle', 'Failed'].includes(stack.state))) { void refresh(); return }
  if (Object.values(protection.value).some(value => value.jobs.some(job => ['running', 'idle', 'pending'].includes(String(job.state))))) void refreshProtection()
}
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
  await refreshProtection()
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
async function openMove(id: string) {
  const stack = stacks.value.find(stack => stack.id === id)
  if (!stack || !canMove(stack) || pendingActions.value.has(id) || movePending(id)) return
  inspectApp(id)
  utility.value = 'move'
  delete moves.value[id]
  moveDestination.value = ''; movePool.value = ''; moveSubmitError.value = ''; movePools.value = []
  if (selectedItem.value?.ports?.length && props.api.listNetworkPools) {
    await read('Network pools', props.api.listNetworkPools(), value => { movePools.value = value })
  }
}
async function pollMove(id: string) {
  const move = moves.value[id]
  if (!active || !move?.operationId || !movePending(id) || pollingMoves.has(id) || !props.api.getOperation) return
  pollingMoves.add(id)
  try {
    const status = await props.api.getOperation(move.operationId)
    if (!active) return
    move.state = status.state
    move.checkpoint = status.checkpoint
    move.error = status.error
    if (!movePending(id)) void refresh()
    if (status.state === 'succeeded' && move.published && props.api.request) {
      const result = await props.api.request(`/stacks/${encodeURIComponent(id)}/status`).catch(() => undefined)
      if (active && result && typeof result === 'object' && 'stack_ip' in result && typeof result.stack_ip === 'string') {
        move.address = result.stack_ip
      }
    }
  } catch (cause) {
    if (!active) return
    if (cause instanceof ApiError && cause.status === 401) { active = false; emit('logout'); return }
    move.error = cause instanceof Error ? cause.message : 'Move status unavailable'
  } finally { pollingMoves.delete(id) }
}
async function submitMove() {
  const stack = selectedStack.value
  const host = moveHosts(stack).find(node => node.id === moveDestination.value)
  if (!active || !stack || !host || !canMove(stack) || !props.api.moveStack || movePending(stack.id)) return
  const pool = eligibleMovePools.value.find(pool => pool.name === movePool.value)
  const request = { destination_node_id: host.id, ...(pool ? { network_pool: pool.name } : {}) }
  const signature = JSON.stringify(request)
  let retry = moveRequestKeys.get(stack.id)
  if (retry?.request !== signature) {
    retry = { request: signature, key: idempotencyKey() }
    moveRequestKeys.set(stack.id, retry)
  }
  moveSubmitError.value = ''
  moves.value[stack.id] = { name: stack.name, host: host.name, published: Boolean(selectedItem.value?.ports?.length), state: 'pending' }
  try {
    const result = await props.api.moveStack(stack.id, request, retry.key)
    if (!active) return
    if (!result?.operation_id) throw new Error('The server returned no move operation ID')
    moveRequestKeys.delete(stack.id)
    moves.value[stack.id]!.operationId = result.operation_id
    await pollMove(stack.id)
  } catch (cause) {
    if (!active) return
    if (cause instanceof ApiError && cause.status === 401) { active = false; emit('logout'); return }
    if (cause instanceof ApiError && cause.status !== 0) moveRequestKeys.delete(stack.id)
    delete moves.value[stack.id]
    const message = cause instanceof Error ? cause.message : 'Move could not be requested'
    if (utility.value === 'move' && selectedApp.value === stack.id) moveSubmitError.value = message
    else errors.value.push(`${stack.name} · ${message}`)
  }
}
async function runAction(args: string[], id: string) {
  if (pendingActions.value.has(id) || movePending(id)) return
  pendingActions.value.add(id)
  try {
    const engine = new CommandEngine(props.api, {
      interactive: false,
      confirm: async () => true,
      editCompose: async () => undefined,
      present: async () => {},
    })
    const { result } = await engine.dispatch(args)
    if (!active) return
    if (result.kind === 'error') {
      if (result.status === 401) { active = false; emit('logout'); return }
      errors.value.push(`${stacks.value.find(stack => stack.id === id)?.name ?? id}: ${result.message}`)
      return
    }
    if (args[2] === 'restartStack') acceptedMutation(`/stacks/${encodeURIComponent(id)}/restart`)
    await refresh()
  } finally { pendingActions.value.delete(id) }
}
function workloadCommand(args: string[], run = true) {
  if (run && ['start', 'stop'].includes(args[0]!) && args[1]) { void runAction(args, args[1]); return }
  commandConsole.value?.launch(args, run)
}
function action(name: string, id?: string) {
  if (name === 'logout') { emit('logout'); return }
  if (name === 'command') { commandConsole.value?.show(); return }
  if (name === 'settings') { commandConsole.value?.launch(['alert-rules', 'list']); return }
  if (id && name === 'app-snapshot-create') { void runAction(['snapshot', 'create', id], id); return }
  if (id && name === 'app-snapshots') { commandConsole.value?.launch(['snapshot', 'list', id]); return }
  if (id && name === 'app-logs') { inspectApp(id); utility.value = 'logs'; return }
  if (id && name === 'app-move') { void openMove(id); return }
  if (id && name === 'app-delete') { commandConsole.value?.launch([name.slice(4), id]); return }
  if (id && name === 'app-host') { const host = apps.value.find(app => app.id === id)?.hostId; if (host) void inspect(host); return }
  if (id && ['app-sync', 'app-backup-run'].includes(name)) {
    const args = ['api', 'store', 'triggerReplication', '--path-stack-id', id]
    const standby = stacks.value.find(stack => stack.id === id)?.secondary_node
    if (name === 'app-sync' && standby) args.push('--query-dest-node-id', standby)
    void runAction(args, id); return
  }
  if (id && ['app-backups', 'app-replication'].includes(name)) { inspectApp(id); utility.value = name.slice(4); return }
  if (id && ['app-start', 'app-stop'].includes(name)) { void runAction([name.slice(4), id], id); return }
  if (id && ['app-restartStack', 'app-triggerFailover', 'app-restoreStack'].includes(name)) { void runAction(['api', 'stacks', name.slice(4), '--path-id', id], id); return }
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
onMounted(() => { void refresh(); timer = setInterval(visibleRefresh, 30_000); protectionTimer = setInterval(refreshActiveJobs, 5_000); moveTimer = setInterval(() => Object.keys(moves.value).forEach(id => { void pollMove(id) }), 2_000); document.addEventListener('visibilitychange', visibleRefresh) })
onBeforeUnmount(() => { active = false; protectionGeneration++; clearInterval(timer); clearInterval(protectionTimer); clearInterval(moveTimer); restartTimers.forEach(clearTimeout); restartTimers.clear(); document.removeEventListener('visibilitychange', visibleRefresh) })
</script>
<template>
  <main class="live-dashboard">
    <LabDashboard :hosts="hosts" :apps="apps" :load-containers="id => api.listContainers(id)" :events="events" :events-open="utility === 'events'" lab-name="My lab" live :loading="loading" :detail-open="Boolean(selectedNode || selectedStack || utility)" @action="action" @closed="restoreFocus">
      <template #command><CommandConsole ref="commandConsole" :api="api" inline @completed="refresh" @mutation="acceptedMutation" /></template>
      <template #messages>
        <div v-if="errors.length" role="alert"><p v-for="error in errors" :key="error">{{ error }}</p></div>
        <template v-for="(move, id) in moves" :key="id">
          <div v-if="utility !== 'move' || selectedApp !== id" role="status">{{ moveMessage(move) }}<span v-if="move.error"> · {{ move.error }}</span></div>
        </template>
        <p v-if="!events.length && !loading">{{ errors.some(error => /^(Events|Activity):/.test(error)) ? 'Events unavailable' : 'No recent events' }}</p>
      </template>
      <template #hosts-empty>
        <p v-if="!hosts.length && !loading">{{ errors.some(error => error.startsWith('Hosts:')) ? 'Hosts unavailable' : 'No registered hosts' }}</p>
      </template>
      <template #detail>
        <DetailPanel v-if="utility === 'node-key'" title="Node key" @close="close"><dl class="node-key"><template v-for="host in hosts" :key="host.id"><dt><UiNodePlate :name="host.name" /></dt><dd>{{ host.name }}<span>{{ host.address || 'Address unavailable' }}</span></dd></template></dl><p v-if="!hosts.length">No registered hosts</p></DetailPanel>
        <DetailPanel v-if="utility === 'events'" title="Recent lab events" @close="close"><LabEventLog :events="events" readonly /></DetailPanel>
        <WorkloadDetail v-if="selectedStack && !['backups', 'replication', 'move'].includes(utility)" :key="selectedStack.id" :app="selectedItem" :initial-tab="utility === 'logs' ? 'logs' : 'containers'" :stack="selectedStack" :api="api" @close="close" @action="name => action(`app-${name}`, selectedStack?.id)" @command="workloadCommand" @updated="refresh" />
        <DetailPanel v-if="utility === 'move'" :title="`Move ${selectedMove?.name ?? selectedStack?.name ?? 'app'}`" @close="close">
          <template v-if="selectedMove">
            <p role="status">{{ moveMessage(selectedMove) }}</p>
            <p v-if="selectedMove.error" :role="selectedMove.state === 'failed' ? 'alert' : 'status'">{{ selectedMove.error }}</p>
          </template>
          <form v-else class="move-form" @submit.prevent="submitMove">
            <label for="move-destination">Destination host</label>
            <UiSelect id="move-destination" v-model="moveDestination" :options="moveHosts(selectedStack).map(node => ({ value: node.id, label: node.name }))" required @change="movePool = ''" />
            <template v-if="eligibleMovePools.length">
              <label for="move-network-pool">Destination network pool (optional)</label>
              <UiSelect id="move-network-pool" v-model="movePool" :options="eligibleMovePools.map(pool => ({ value: pool.name, label: pool.name }))" />
            </template>
            <p>The app will briefly go offline during the move. Published services will get a new address.</p>
            <p v-if="moveSubmitError" role="alert">{{ moveSubmitError }}</p>
            <UiButton variant="primary" type="submit" :disabled="!moveDestination">Move app</UiButton>
          </form>
        </DetailPanel>
        <DetailPanel v-if="selectedStack && utility === 'backups' && backupApi" title="Backups and recovery" @close="close"><BackupTargets :key="selectedStack.id" :api="backupApi" :nodes="nodes" :stacks="[selectedStack]" @restored="refresh" /></DetailPanel>
        <DetailPanel v-if="selectedStack && utility === 'replication'" title="Replication" @close="close">
          <AppPlateActions v-if="selectedItem" :app="selectedItem" kind="replication" @action="name => action(`app-${name}`, selectedStack?.id)" />
          <p v-if="protection[selectedStack.id]?.error" role="alert">{{ protection[selectedStack.id]?.error }}</p>
          <p v-if="!protection[selectedStack.id]?.jobs.length">No replication jobs</p>
          <ul class="replication-history"><li v-for="job in protection[selectedStack.id]?.jobs" :key="String(job.id)"><UiPlateCard kind="replication" :label="String(job.state)" :busy="['running', 'idle', 'pending'].includes(String(job.state))" :detail="String(job.error || job.started_at || job.created_at || '')" disabled /><span>{{ job.dataset }} → {{ nodes.find(node => node.id === job.dest_node_id)?.name ?? job.dest_node_id }}</span><p v-if="job.error" role="alert">{{ job.error }}</p></li></ul>
        </DetailPanel>
        <LiveHostDetail v-if="selectedNode" :key="selectedNode.id" :node="selectedNode" :apps="selectedApps" :storage="storage[selected]" :logs="logs[selected]" :loading="loading" @close="close" />
        <ApplianceUpdates v-if="selectedNode && updateApi" :key="selectedNode.id + '-updates'" :api="updateApi" :node-id="selectedNode.id" />
      </template>
      <template #footer-actions><div class="dashboard-controls"><span v-if="raft?.dev_mode" role="status">Development mode</span></div></template>
    </LabDashboard>
    <CreateApp v-if="creating" :nodes="nodes" @close="creating = false" @create="createApp" />
  </main>
</template>
<style scoped>
.live-dashboard { width: 100%; margin: 0; padding: 0; min-height: 100vh; }
.dashboard-controls { display: flex; align-items: center; gap: 16px; margin-left: auto; flex-wrap: wrap; }
.node-key { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 16px; }
.node-key dd { margin: 0; overflow-wrap: anywhere; }
.node-key dd span { display: block; margin-top: 4px; color: var(--text-placeholder); font-size: var(--text-meta); }
.replication-history { list-style: none; padding: 0; }
.replication-history li { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 8px 0; border-bottom: 1px solid var(--line); }
.move-form { display: grid; gap: 8px; justify-items: start; }
[role="alert"] { color: var(--status-error); }
</style>
