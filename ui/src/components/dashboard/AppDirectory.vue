<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { Container } from '@floatlab/client'
import { IconArrowDown, IconArrowUp, IconChevronDown, IconBox } from '@tabler/icons-vue'
import type { DirectoryApp } from '../../dashboard/types'
import UiButton from '../UiButton.vue'
import AppPlateActions from './AppPlateActions.vue'

const props = defineProps<{ apps: DirectoryApp[]; readonly?: boolean; managed?: boolean; loadContainers?: (id: string) => Promise<Container[]> }>()
const columns = ['App', 'IP address', 'Ports', 'Runtime', 'Replication', 'Backup'] as const
type Column = typeof columns[number]
const sortBy = ref<Column>('App')
const descending = ref(false)
const runtimeOrder = ['Failed', 'FailingOver', 'Restoring', 'RollingBack', 'Updating', 'Provisioning', 'Starting', 'Stopping', 'Restarting', 'Stopped', 'Running']
const protectionOrder = ['Failed', 'Offline', 'Unavailable', 'Unknown', 'Not synced', 'Syncing', 'Queued', 'In use', 'None', 'Scheduled', 'Synced']
function sortValue(app: DirectoryApp) {
  if (sortBy.value === 'App') return app.name
  if (sortBy.value === 'IP address') return app.address || ''
  if (sortBy.value === 'Ports') return app.ports?.join(', ') ?? ''
  if (sortBy.value === 'Runtime') return app.runtimeMotion ? 'Restarting' : app.state === 'Idle' ? 'Stopped' : ['RunningPrimary', 'RunningBackup'].includes(app.state) ? 'Running' : app.state
  return sortBy.value === 'Replication' ? app.replication?.label ?? (app.standby ? 'Not synced' : 'None') : app.backups?.label ?? (app.backup ? 'Unknown' : 'None')
}
const sortedApps = computed(() => [...props.apps].sort((a, b) => {
  const av = sortValue(a), bv = sortValue(b)
  const order = sortBy.value === 'Runtime' ? runtimeOrder : protectionOrder
  const comparison = ['App', 'IP address', 'Ports'].includes(sortBy.value) ? av.localeCompare(bv, undefined, { numeric: sortBy.value !== 'App' }) : order.indexOf(av) - order.indexOf(bv)
  return (descending.value ? -comparison : comparison) || a.name.localeCompare(b.name) || a.id.localeCompare(b.id)
}))
function sort(column: Column) {
  descending.value = sortBy.value === column ? !descending.value : false
  sortBy.value = column
}
const emit = defineEmits<{ open: [appId: string]; command: [appId: string, action: string] }>()
const selected = ref('')
const expanded = ref(false)
const containers = ref<Container[]>([])
const loading = ref(false)
const error = ref('')
const directory = ref<HTMLElement>()
let generation = 0
watch([selected, () => props.apps], async () => {
  const request = ++generation
  containers.value = []; error.value = ''; loading.value = false
  if (!props.loadContainers || !selected.value || !props.apps.some(app => app.id === selected.value)) return
  loading.value = true
  try {
    const result = await props.loadContainers(selected.value)
    if (request === generation) containers.value = result
  } catch (cause) { if (request === generation) error.value = cause instanceof Error ? cause.message : 'Containers unavailable' }
  finally { if (request === generation) loading.value = false }
})
function select(id: string) { selected.value = id; expanded.value = true; emit('open', id) }
async function navigate(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  const row = target.closest<HTMLElement>('.app-row')
  if (props.readonly || !row || event.altKey || event.metaKey || event.ctrlKey || target.closest('button:not(.app-open), a, input, textarea, select, [contenteditable], .dropdown-trigger')) return
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End', 'ArrowLeft', 'ArrowRight'].includes(event.key)) return
  event.preventDefault(); event.stopPropagation()
  if (event.key === 'ArrowLeft') { expanded.value = false; return }
  const index = sortedApps.value.findIndex(app => app.id === row.dataset.appId)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? sortedApps.value.length - 1 : event.key === 'ArrowRight' ? index : Math.max(0, Math.min(sortedApps.value.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))
  const app = sortedApps.value[next]
  if (!app) return
  select(app.id)
  await nextTick()
  const element = Array.from(directory.value?.querySelectorAll<HTMLElement>('.app-row') ?? []).find(row => row.dataset.appId === app.id)
  element?.focus({ preventScroll: true })
  element?.scrollIntoView?.({ block: 'nearest' })
}
async function openRow(event: globalThis.MouseEvent, id: string) {
  const row = event.currentTarget as HTMLElement
  if (props.readonly || (event.target as HTMLElement).closest('button, a, .dropdown-trigger')) return
  if (selected.value === id && expanded.value) expanded.value = false
  else select(id)
  await nextTick()
  if (row.offsetParent) row.focus({ preventScroll: true })
}
</script>

<template>
  <div class="directory-scroll">
    <div ref="directory" class="app-directory" role="table" aria-label="Apps" @keydown="navigate">
      <div class="app-columns" role="row"><span v-for="column in columns" :key="column" role="columnheader" :aria-sort="sortBy === column ? descending ? 'descending' : 'ascending' : 'none'"><UiButton variant="plain" class="column-sort" :aria-label="`Sort by ${column}`" @click="sort(column)">{{ column }}<component :is="descending ? IconArrowDown : IconArrowUp" v-if="sortBy === column" :size="12" aria-hidden="true" /></UiButton></span></div>
      <template v-for="app in sortedApps" :key="app.id">
        <div class="app-row" :data-app-id="app.id" :class="{ readonly, selected: selected === app.id }" :aria-expanded="readonly ? undefined : selected === app.id && expanded" role="row" :tabindex="readonly ? undefined : 0" @click="openRow($event, app.id)" @keydown.enter.self="!readonly && emit('open', app.id)" @keydown.space.self.prevent="!readonly && emit('open', app.id)">
          <div class="app-identity" role="cell"><IconChevronDown v-if="!readonly" class="expand-icon" :class="{ expanded: selected === app.id && expanded }" :size="12" aria-hidden="true" /><span v-if="readonly">{{ app.name }}</span><UiButton v-else variant="plain" class="app-open" :aria-label="`Open ${app.name}`" @click="emit('open', app.id)">{{ app.name }}</UiButton><small v-if="app.hostSwitch === 'restoreStack' || app.state === 'RunningBackup'" class="standby-note">Serving from standby</small></div>
          <span class="app-address" role="cell">{{ app.address || '—' }}</span>
          <span class="app-ports" role="cell"><span v-for="port in app.ports" :key="port" class="port-chip">{{ port }}</span><span v-if="!app.ports?.length">—</span></span>
          <span v-for="kind in ['runtime', 'replication', 'backup'] as const" :key="kind" role="cell" :class="`app-${kind}`"><AppPlateActions :app="app" :kind="kind" :readonly="readonly" @action="emit('command', app.id, $event)" /></span>
        </div>
        <div v-if="selected === app.id && expanded" class="app-services" role="row">
          <div role="cell" aria-colspan="6">
            <p v-if="loading" role="status">Loading containers…</p>
            <p v-else-if="error" role="alert">{{ error }}</p>
            <p v-else-if="!containers.length">{{ loadContainers ? 'No containers' : 'Container details unavailable' }}</p>
            <UiButton v-for="container in containers" :key="container.id" variant="plain" class="service-row" @click="emit('open', app.id)"><IconBox :size="14" aria-hidden="true" /><span>{{ container.service || container.name }}</span><small>{{ container.image }}</small><span class="service-state">{{ container.status }}<template v-if="container.health && container.health !== 'none'"> · {{ container.health }}</template></span></UiButton>
          </div>
        </div>
      </template>
      <p v-if="!apps.length" class="directory-empty">{{ readonly || managed ? 'No apps' : 'No apps match these filters.' }}</p>
    </div>
  </div>
</template>

<style scoped>
.directory-scroll { container-type: inline-size; overflow-x: auto; }
.app-directory { min-width: 1140px; }
.app-columns, .app-row { display: grid; grid-template-columns: minmax(160px, 1fr) 150px 100px 180px 200px 240px; gap: 16px; align-items: center; }
.app-columns { height: 42px; padding: 0 12px; color: var(--text-secondary); font-size: var(--text-column); line-height: var(--line-column); letter-spacing: var(--tracking-column); text-transform: uppercase; }
/* Keep plate fills distinct from the solid row surface. */
.app-row { --plate-surface: var(--surface-recessed); min-height: 48px; margin-bottom: 2px; padding: 8px 12px; border-radius: var(--radius); background: var(--surface-default); color: var(--text-secondary); font-size: var(--text-status); line-height: var(--line-body); }
.app-row.selected { background: color-mix(in srgb, var(--surface-default) 90%, var(--text-secondary)); }
.app-row:focus-visible { outline: 1px solid var(--green); outline-offset: -1px; }
.app-row:not(.readonly) { cursor: pointer; transition: background-color 120ms ease-out; }
.app-row:not(.readonly):hover { background: color-mix(in srgb, var(--surface-default) 90%, var(--text-secondary)); }
.app-row > [role="cell"] { min-width: 0; }
.app-identity { position: relative; padding-left: 18px; display: flex; flex-direction: column; align-items: flex-start; overflow-wrap: anywhere; }
.app-address { overflow-wrap: anywhere; }
.app-ports { display: flex; flex-wrap: wrap; gap: 4px; }
.port-chip { padding: 0 4px; border-radius: 1px; background: var(--surface-recessed); font-size: 10px; }
.standby-note { color: var(--status-warning); font-size: 9px; line-height: 12px; }
.column-sort { display: inline-flex; align-items: center; gap: 8px; min-height: 32px; color: inherit; font: inherit; letter-spacing: inherit; text-transform: inherit; }
.column-sort svg { color: inherit; }
.app-open { color: var(--text-primary); font-size: var(--text-item); text-align: left; }
.expand-icon { position: absolute; left: 0; top: 6px; transform: rotate(-90deg); }
.expand-icon.expanded { transform: none; }
.app-services { margin: -2px 0 2px; padding: 8px 12px 8px 30px; background: var(--surface-recessed); font-size: var(--text-meta); }
.app-services p { margin: 8px 0; }
.service-row { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 32px; text-align: left; }
.service-row small { color: var(--text-placeholder); }
.service-state { margin-left: auto; }
.directory-empty { padding: var(--space-5); text-align: center; }
</style>
