<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, type DashboardApi, type FloatLabApi, type Node, type Stack, type NodeStats, type StorageStats, type Notification, type RaftHealth, type LogLine } from '@floatlab/client'
import { appItem, hostItem, labEvent } from '../../dashboard/live'
import UiButton from '../UiButton.vue'
import SplitView from '../SplitView.vue'
import CommandConsole from '../CommandConsole.vue'
import UiAssetIcon from './UiAssetIcon.vue'
import HostDirectory from './HostDirectory.vue'
import AppDirectory from './AppDirectory.vue'
import LabEventLog from './LabEventLog.vue'
import LiveHostDetail from './LiveHostDetail.vue'
const props = defineProps<{ api: DashboardApi & FloatLabApi }>()
const emit = defineEmits<{ logout: [] }>()
const nodes = ref<Node[]>([])
const stacks = ref<Stack[]>([])
const notifications = ref<Notification[]>([])
const raft = ref<RaftHealth>()
const stats = ref<Record<string, NodeStats>>({})
const storage = ref<Record<string, StorageStats>>({})
const logs = ref<Record<string, LogLine[]>>({})
const loading = ref(false)
const errors = ref<string[]>([])
const selected = ref('')
const commandConsole = ref<InstanceType<typeof CommandConsole>>()
const hosts = computed(() => nodes.value.map(node => hostItem(node, stacks.value, stats.value[node.id], storage.value[node.id])))
const apps = computed(() => stacks.value.map(stack => appItem(stack, nodes.value)))
const events = computed(() => notifications.value.map(notification => labEvent(notification, nodes.value)))
const selectedNode = computed(() => nodes.value.find(node => node.id === selected.value))
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
  selected.value = id
  await read('Host logs', props.api.getNodeLogs(id), value => { logs.value[id] = value })
}
async function close() { selected.value = ''; await nextTick(); origin?.focus({ preventScroll: true }) }
function restoreFocus() { origin?.focus({ preventScroll: true }) }
function visibleRefresh() { if (document.visibilityState === 'visible') void refresh() }
onMounted(() => { void refresh(); timer = setInterval(visibleRefresh, 30_000); document.addEventListener('visibilitychange', visibleRefresh) })
onBeforeUnmount(() => { active = false; clearInterval(timer); document.removeEventListener('visibilitychange', visibleRefresh) })
</script>
<template>
  <main id="dashboard" class="live-dashboard">
    <header>
      <UiAssetIcon src="/assets/dashboard/imgFloatLabLogo.svg" :size="34" /><h1>FloatLab</h1>
      <UiButton variant="surface" @click="commandConsole?.show()">Command Palette</UiButton>
      <UiButton variant="surface" :disabled="loading" @click="refresh">Refresh</UiButton>
      <UiButton variant="surface" @click="emit('logout')">Sign out</UiButton>
    </header>
    <p role="status">{{ loading ? 'Refreshing…' : cluster }}</p>
    <div v-if="errors.length" role="alert"><p v-for="error in errors" :key="error">{{ error }}</p></div>
    <LabEventLog :events="events" readonly />
    <p v-if="!events.length && !loading">{{ errors.some(error => error.startsWith('Events:')) ? 'Events unavailable' : 'No recent events' }}</p>
    <SplitView :open="Boolean(selectedNode)" @closed="restoreFocus">
      <div>
        <h2>Hosts</h2><HostDirectory :hosts="hosts" @inspect="inspect" @tools="inspect" @connect="inspect" />
        <p v-if="!hosts.length && !loading">{{ errors.some(error => error.startsWith('Hosts:')) ? 'Hosts unavailable' : 'No registered hosts' }}</p>
        <h2>Apps</h2><AppDirectory :apps="apps" readonly />
      </div>
      <template #detail>
        <LiveHostDetail v-if="selectedNode" :key="selectedNode.id" :node="selectedNode" :apps="selectedApps" :storage="storage[selected]" :logs="logs[selected]" :loading="loading" @close="close" />
      </template>
    </SplitView>
    <CommandConsole ref="commandConsole" :api="api" />
  </main>
</template>
<style scoped>
.live-dashboard { display: grid; gap: 20px; padding: 32px 48px; }
header { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; } h1 { margin: 0 auto 0 0; }
[role="alert"] { color: var(--status-error); } @media (max-width: 900px) { .live-dashboard { padding: 16px; } }
</style>
