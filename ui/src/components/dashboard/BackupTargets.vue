<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { backupRestoreDatasets } from '@floatlab/client'
import type { BackupApi, BackupRestoreApi, BackupTarget, Node, Stack } from '@floatlab/client'
import UiButton from '../UiButton.vue'
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
const restoreAvailable = computed(() => props.api.listBackups && props.api.getBackupConfig && props.api.restoreBackup)
let generation = 0
let active = true
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
  if (busy.value || !stack.value) return
  const version = ++generation
  busy.value = true; error.value = ''
  const id = stack.value
  const selected = target ?? { dataset: dataset.value, node_id: node.value, schedule: schedule.value }
  try {
    const key = globalThis.crypto.randomUUID()
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
    await props.api.restoreBackup({name: restoredName.value, source_node_id: props.stacks.find(item => item.id === stack.value)!.primary_node, backup_node_id: backupNode.value, destination_node_id: destination.value, compose_file: compose.value, snapshots: restoreDatasets.value.map(dataset => ({dataset, snapshot: restorePoint.value}))}, globalThis.crypto.randomUUID())
    if (active && version === generation) { restoreMessage.value = 'Restore and start queued. Track the new app in the dashboard.'; emit('restored') }
  } catch (cause) { if (active && version === generation) error.value = cause instanceof Error ? cause.message : 'Restore failed' }
  finally { if (active && version === generation) busy.value = false }
}
watch([compose, backupNode], () => { restorePoints.value = []; restorePoint.value = ''; restoreDatasets.value = [] })
watch(stack, () => { dataset.value = ''; compose.value = ''; restoreMessage.value = ''; void refresh() })
onBeforeUnmount(() => { active = false; generation++ })
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
      <UiButton :disabled="busy || !dataset || !node || !schedule" @click="change('save')">Save backup target</UiButton>
      <UiButton :disabled="busy" @click="refresh">Refresh backups</UiButton>
      <ul>
        <li v-for="target in targets" :key="target.dataset + target.node_id">
          {{ target.dataset }} → {{ nodes.find(item => item.id === target.node_id)?.name ?? target.node_id }} · {{ target.schedule }} · {{ target.enabled ? 'Enabled' : target.cleanup_pending ? 'Cleanup pending' : 'Removed' }}
          <p v-if="target.error" role="alert">{{ target.error }}</p>
          <UiButton v-if="target.enabled" :disabled="busy" @click="change('run', target)">Replicate now</UiButton>
          <UiButton v-if="target.enabled" :disabled="busy" @click="change('remove', target)">Remove target</UiButton>
        </li>
      </ul>
      <p v-if="!targets.length && !busy">No backup targets configured</p>
      <ul aria-label="Replication history"><li v-for="job in history" :key="String(job.id)">{{ job.dataset }} → {{ nodes.find(item => item.id === job.dest_node_id)?.name ?? job.dest_node_id }} · {{ job.state }}<span v-if="job.error"> · {{ job.error }}</span></li></ul>
      <section v-if="restoreAvailable" aria-label="Restore backup" class="backup-targets">
        <h3>Restore a separate app</h3>
        <label>Backup node <UiSelect v-model="backupNode" aria-label="Restore backup node" :disabled="busy" :options="nodes.map(item => ({value: item.id, label: item.name}))" /></label>
        <p>Use the current saved Compose file or edit it to match this backup.</p>
        <label>Compose <UiTextarea v-model="compose" aria-label="Restore Compose" :disabled="busy" rows="8" /></label>
        <UiButton :disabled="busy || !backupNode" @click="loadRestorePoints">Load restore points</UiButton>
        <label>Snapshot <UiSelect v-model="restorePoint" aria-label="Restore snapshot" :disabled="busy" :options="restorePoints.map(value => ({value, label: value}))" /></label>
        <label>Run on node <UiSelect v-model="destination" aria-label="Restore destination node" :disabled="busy" :options="nodes.map(item => ({value: item.id, label: item.name}))" /></label>
        <label>New app name <UiInput v-model="restoredName" aria-label="Restored app name" :disabled="busy" /></label>
        <UiButton :disabled="busy || !restorePoint || !destination || !restoredName" @click="restore">Restore and start</UiButton>
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
