<script setup lang="ts">
import { computed } from 'vue'
import type { DirectoryApp, TransferStatus } from '../../dashboard/types'
import UiDropdown from '../UiDropdown.vue'
import UiPlateCard from '../UiPlateCard.vue'

const props = defineProps<{ app: DirectoryApp; kind?: 'runtime' | 'replication' | 'backup'; readonly?: boolean }>()
const emit = defineEmits<{ action: [action: string] }>()
const running = computed(() => ['Running', 'RunningPrimary', 'RunningBackup'].includes(props.app.state))
const busy = computed(() => Boolean(props.app.actionPending || props.app.runtimeMotion) || !['Running', 'RunningPrimary', 'RunningBackup', 'Stopped', 'Idle', 'Failed'].includes(props.app.state))
const runtimeLabel = computed(() => props.app.runtimeMotion ? 'Restarting' : props.app.state === 'Idle' ? 'Stopped' : running.value ? 'Running' : props.app.state)
const kinds = computed(() => props.kind ? [props.kind] : ['runtime', 'replication', 'backup'] as const)
function status(kind: string): TransferStatus {
  if (kind === 'runtime') return { label: runtimeLabel.value, host: props.app.host, hostHealth: props.app.hostHealth, busy: busy.value, tone: props.app.state === 'Failed' ? 'error' : busy.value ? 'warning' : 'default' }
  return kind === 'replication' ? props.app.replication ?? { label: props.app.standby ? 'Not synced' : 'None', host: props.app.standby } : props.app.backups ?? { label: props.app.backup ? 'Unknown' : 'None', host: props.app.backup }
}
function items(kind: string) {
  const locked = Boolean(props.app.replica)
  if (kind === 'runtime') return [
    { id: running.value ? 'stop' : 'start', label: running.value ? 'Stop' : 'Start', disabled: locked || busy.value },
    { id: 'restartStack', label: 'Restart', disabled: locked || !running.value || busy.value },
    { id: 'delete', label: 'Delete app…', disabled: locked || busy.value },
    { id: 'logs', label: 'View logs' },
    { id: 'open', label: 'App details' },
    { id: 'host', label: `Open host · ${props.app.host || 'Unknown'}`, disabled: !props.app.hostId },
    ...(props.app.hostSwitch ? [{ id: props.app.hostSwitch, label: `${props.app.hostSwitch === 'restoreStack' ? 'Restore primary' : 'Fail over'} · ${props.app.standby}`, disabled: locked || busy.value }] : []),
  ]
  const available = props.app.protectionAvailable !== false
  if (kind === 'replication') return [
    { id: 'sync', label: 'Sync now', disabled: locked || props.app.hostSwitch === 'restoreStack' || !props.app.standby || !available || Boolean(status(kind).busy) },
    { id: 'replication', label: 'View replication history', disabled: !available },
    ...(props.app.hostSwitch ? [{ id: props.app.hostSwitch, label: props.app.hostSwitch === 'restoreStack' ? 'Restore primary' : 'Fail over to standby', disabled: locked || busy.value }] : []),
  ]
  return [
    { id: 'backups', label: 'Recovery points', disabled: !available },
    { id: 'snapshots', label: 'View snapshots' },
    { id: 'snapshot-create', label: 'Create snapshot', disabled: locked },
    { id: 'backup-run', label: 'Back up now', disabled: locked || props.app.hostSwitch === 'restoreStack' || !available || !status(kind).host || Boolean(status(kind).busy) },
    { id: 'backups', label: 'Configure backups', disabled: locked || !available },
  ]
}
</script>

<template>
  <span v-for="plateKind in kinds" :key="plateKind" class="app-plate-action" :data-plate-kind="plateKind">
    <UiPlateCard v-if="readonly" v-bind="status(plateKind)" :kind="plateKind" :active="plateKind === 'runtime' && running" :motion="plateKind === 'runtime' ? app.runtimeMotion : undefined" disabled :aria-label="`${app.name} ${plateKind}: ${plateKind === 'backup' && status(plateKind).label === 'Synced' ? 'Backed up' : status(plateKind).label}`" />
    <UiDropdown v-else :label="`${app.name} ${plateKind}`" :heading="app.name" :summary="status(plateKind).label" :items="items(plateKind)" @select="emit('action', $event)">
      <template #trigger="{ toggle, open, controls }"><UiPlateCard v-bind="status(plateKind)" :disabled="app.actionPending" :kind="plateKind" :active="plateKind === 'runtime' && running" :motion="plateKind === 'runtime' ? app.runtimeMotion : undefined" :aria-label="`${app.name} ${plateKind}: ${plateKind === 'backup' && status(plateKind).label === 'Synced' ? 'Backed up' : status(plateKind).label}`" aria-haspopup="menu" :aria-expanded="open" :aria-controls="controls" @click="toggle" /></template>
    </UiDropdown>
  </span>
</template>

<style scoped>
.app-plate-action { display: inline-flex; min-width: 0; }
</style>
