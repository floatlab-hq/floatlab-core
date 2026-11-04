<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { backupRestoreDatasets, idempotencyKey } from '@floatlab/client'
import type { BackupApi, BackupRestoreApi, BackupTarget, Node, Stack } from '@floatlab/client'
import UiButton from '../UiButton.vue'
import UiDropdown from '../UiDropdown.vue'
import UiPlateCard from '../UiPlateCard.vue'
import { transferStatus } from '../../dashboard/protection'
import UiInput from '../UiInput.vue'
import UiSelect from '../UiSelect.vue'
import UiTextarea from '../UiTextarea.vue'
const props = defineProps<{ api: BackupApi & Partial<BackupRestoreApi>; stacks: Stack[]; nodes: Node[] }>()
const emit = defineEmits<{ restored: [] }>()
const stack = ref('')
const dataset = ref('')
const node = ref('')
const schedule = ref('h/24 d/28')
const targets = ref<BackupTarget[]>([])
const history = ref<Record<string, unknown>[]>([])
const busy = ref(false)
const error = ref('')
const backupNode = ref('')
const destination = ref('')
const restoredName = ref('')
const compose = ref('')
const restorePoint = ref('')
const restorePoints = ref<string[]>([])
const restoreDatasets = ref<string[]>([])
const restoreMessage = ref('')
const writable = computed(() => !props.stacks.find(item => item.id === stack.value)?.replica)
const restoreAvailable = computed(() => props.api.listBackups && props.api.getBackupConfig && props.api.restoreBackup)
let generation = 0
let active = true
let poll: ReturnType<typeof setInterval> | undefined
watch(() => props.stacks, stacks => { if (stacks.length === 1 && stack.value !== stacks[0]!.id) stack.value = stacks[0]!.id })
async function refresh() {
  const version = ++generation
  if (!stack.value) { targets.value = []; history.value = []; return }
  busy.value = true; error.value = ''
  try {
    const [current, jobs] = await Promise.all([props.api.listBackupTargets(stack.value), props.api.listReplication(stack.value)])
    if (active && version === generation) { targets.value = current; history.value = jobs }
  } catch (cause) { if (active && version === generation) error.value = cause instanceof Error ? cause.message : 'Backup status unavailable' }
  finally { if (active && version === generation) busy.value = false }
}
async function change(action: 'save' | 'remove' | 'run', target?: BackupTarget) {
  if (busy.value || !stack.value || !writable.value) return
  const version = ++generation
  busy.value = true; error.value = ''
  const id = stack.value
  const selected = target ?? { dataset: dataset.value, node_id: node.value, schedule: schedule.value }
  try {
    const key = idempotencyKey()
    if (action === 'save') await props.api.putBackupTarget(id, selected, key)
    else if (action === 'remove') await props.api.removeBackupTarget(id, selected, key)
    else await props.api.triggerReplication(id, selected.node_id, selected.dataset, key)
    if (active && version === generation) { busy.value = false; await refresh() }
  } catch (cause) { if (active && version === generation) error.value = cause instanceof Error ? cause.message : 'Backup action failed' }
  finally { if (active && version === generation) busy.value = false }
}
async function loadRestorePoints() {
  if (busy.value || !stack.value || !backupNode.value || !props.api.listBackups || !props.api.getBackupConfig) return
  const version = ++generation
  busy.value = true; error.value = ''; restoreMessage.value = ''; restorePoints.value = []
  try {
    const source = compose.value || await props.api.getBackupConfig(stack.value)
    if (!active || version !== generation) return
    compose.value = source
    const datasets = backupRestoreDatasets(source)
    const sourceNode = props.stacks.find(item => item.id === stack.value)!.primary_node
    const snapshots = await Promise.all(datasets.map(dataset => props.api.listBackups!({node_id: backupNode.value, source_node_id: sourceNode, dataset})))
    if (!active || version !== generation) return
    restoreDatasets.value = datasets
    restorePoints.value = [...new Set(snapshots[0]!.map(snapshot => snapshot.name))].filter(name => snapshots.every(items => items.some(item => item.name === name))).sort().reverse()
    if (!restorePoints.value.length) error.value = 'No common snapshot covers every writable mount. Select another backup or supply its matching Compose file.'
  } catch (cause) { if (active && version === generation) error.value = cause instanceof Error ? cause.message : 'Backup snapshots unavailable' }
  finally { if (active && version === generation) busy.value = false }
}
async function restore() {
  if (busy.value || !restorePoint.value || !restoredName.value || !destination.value || !props.api.restoreBackup) return
  const version = ++generation
  busy.value = true; error.value = ''; restoreMessage.value = ''
  try {
    await props.api.restoreBackup({name: restoredName.value, source_node_id: props.stacks.find(item => item.id === stack.value)!.primary_node, backup_node_id: backupNode.value, destination_node_id: destination.value, compose_file: compose.value, snapshots: restoreDatasets.value.map(dataset => ({dataset, snapshot: restorePoint.value}))}, idempotencyKey())
    if (active && version === generation) { restoreMessage.value = 'Restore and start queued. Track the new app in the dashboard.'; emit('restored') }
  } catch (cause) { if (active && version === generation) error.value = cause instanceof Error ? cause.message : 'Restore failed' }
  finally { if (active && version === generation) busy.value = false }
}
watch([compose, backupNode], () => { restorePoints.value = []; restorePoint.value = ''; restoreDatasets.value = [] })
watch(stack, () => { dataset.value = ''; compose.value = ''; restoreMessage.value = ''; void refresh() })
onMounted(() => { if (props.stacks.length === 1) stack.value = props.stacks[0]!.id; poll = setInterval(() => { if (!busy.value && document.visibilityState === 'visible' && history.value.some(job => ['running', 'idle', 'pending'].includes(String(job.state)))) void refresh() }, 5_000) })
onBeforeUnmount(() => { active = false; generation++; clearInterval(poll) })
</script>
<template>
  <section aria-label="Backup destinations" class="backup-targets">
    <h2>Backup destinations</h2>
    <label>App <UiSelect v-model="stack" :disabled="busy" :options="stacks.map(item => ({ value: item.id, label: item.name }))" /></label>
    <template v-if="stack">
      <p>Backups receive read-only datasets. Replication rolls back destination-only changes automatically.</p>
      <label>Dataset <UiInput v-model="dataset" :disabled="busy" placeholder="floatlab/app/data" /></label>
      <label>Destination <UiSelect v-model="node" :disabled="busy" :options="nodes.filter(item => item.id !== stacks.find(app => app.id === stack)?.primary_node).map(item => ({ value: item.id, label: item.name }))" /></label>
      <label>Schedule and retention <UiInput v-model="schedule" :disabled="busy" /></label>
      <UiPlateCard kind="backup" label="Save backup target" :disabled="busy || !writable || !dataset || !node || !schedule" @click="change('save')" />
      <UiButton :disabled="busy" @click="refresh">Refresh backups</UiButton>
      <ul>
        <li v-for="target in targets" :key="target.dataset + target.node_id">
          {{ target.dataset }} → {{ nodes.find(item => item.id === target.node_id)?.name ?? target.node_id }} · {{ target.schedule }} · {{ target.enabled ? 'Enabled' : target.cleanup_pending ? 'Cleanup pending' : 'Removed' }}
          <p v-if="target.error" role="alert">{{ target.error }}</p>
          <UiDropdown v-if="target.enabled" :label="`Backup actions for ${target.dataset}`" :heading="target.dataset" :items="[{id: 'run', label: 'Replicate now', disabled: busy || !writable}, {id: 'remove', label: 'Remove target', disabled: busy || !writable}]" @select="name => change(name as 'run' | 'remove', target)">
            <template #trigger="{ toggle, open, controls }"><UiPlateCard kind="backup" v-bind="transferStatus(history.filter(job => job.dest_node_id === target.node_id && job.dataset === target.dataset), 'Scheduled')" :host="nodes.find(item => item.id === target.node_id)?.name ?? target.node_id" :host-health="nodes.find(item => item.id === target.node_id)?.status" aria-haspopup="menu" :aria-expanded="open" :aria-controls="controls" @click="toggle" /></template>
          </UiDropdown>
        </li>
      </ul>
      <p v-if="!targets.length && !busy">No backup targets configured</p>
      <ul aria-label="Replication history"><li v-for="job in history" :key="String(job.id)"><UiPlateCard kind="backup" v-bind="transferStatus([job])" :host="nodes.find(item => item.id === job.dest_node_id)?.name ?? String(job.dest_node_id ?? '')" disabled /> {{ job.dataset }}<span v-if="job.error"> · {{ job.error }}</span></li></ul>
      <section v-if="restoreAvailable" aria-label="Restore backup" class="backup-targets">
        <h3>Restore a separate app</h3>
        <label>Backup node <UiSelect v-model="backupNode" aria-label="Restore backup node" :disabled="busy" :options="nodes.map(item => ({value: item.id, label: item.name}))" /></label>
        <p>Use the current saved Compose file or edit it to match this backup.</p>
        <label>Compose <UiTextarea v-model="compose" aria-label="Restore Compose" :disabled="busy" rows="8" /></label>
        <UiButton :disabled="busy || !backupNode" @click="loadRestorePoints">Load restore points</UiButton>
        <label>Snapshot <UiSelect v-model="restorePoint" aria-label="Restore snapshot" :disabled="busy" :options="restorePoints.map(value => ({value, label: value}))" /></label>
        <label>Run on node <UiSelect v-model="destination" aria-label="Restore destination node" :disabled="busy" :options="nodes.map(item => ({value: item.id, label: item.name}))" /></label>
        <label>New app name <UiInput v-model="restoredName" aria-label="Restored app name" :disabled="busy" /></label>
        <UiPlateCard kind="backup" label="Restore and start" :disabled="busy || !restorePoint || !destination || !restoredName" @click="restore" />
        <p v-if="restoreMessage" role="status">{{ restoreMessage }}</p>
      </section>
    </template>
    <p v-if="busy" role="status">Checking backups…</p>
    <p v-if="error" role="alert">{{ error }}</p>
  </section>
</template>
<style scoped>
.backup-targets { display: grid; gap: 12px; }
label { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
</style>
