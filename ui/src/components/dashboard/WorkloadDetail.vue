<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { idempotencyKey, type Container, type FloatLabApi, type LogLine, type Stack, type StackMetricSeries } from '@floatlab/client'
import DetailPanel from '../DetailPanel.vue'
import UiButton from '../UiButton.vue'
import UiDropdown from '../UiDropdown.vue'
import UiInput from '../UiInput.vue'
import UiPlateCard from '../UiPlateCard.vue'
import UiSelect from '../UiSelect.vue'
import UiTabs from '../UiTabs.vue'
import UiTextarea from '../UiTextarea.vue'
import AppPlateActions from './AppPlateActions.vue'
import { appItem } from '../../dashboard/live'
import type { DirectoryApp } from '../../dashboard/types'
const props = withDefaults(defineProps<{ stack: Stack; api: FloatLabApi; app?: DirectoryApp; initialTab?: string }>(), { app: undefined, initialTab: 'containers' })
const emit = defineEmits<{ close: []; command: [args: string[], run?: boolean]; action: [action: string]; updated: [] }>()
const item = computed(() => props.app ?? appItem(props.stack, []))
const tab = ref(props.initialTab)
const tabs = computed(() => [
  { id: 'containers', label: 'Containers' }, { id: 'logs', label: 'Logs' }, { id: 'metrics', label: 'Metrics' },
  { id: 'compose', label: 'Compose' }, { id: 'inspect', label: 'Inspect' },
  ...props.stack.replica ? [] : [{ id: 'edit', label: 'Edit Compose' }, { id: 'upgrade', label: 'Upgrade images' }],
])
const containers = ref<Container[]>([])
const error = ref('')
const loading = ref(true)
const tabError = ref('')
const tabLoading = ref(false)
const logs = ref<LogLine[]>([])
const metrics = ref<StackMetricSeries[]>([])
const inspected = ref<Stack>()
const compose = ref<string>()
const draft = ref('')
const range = ref('1h')
const logContainer = ref('all')
const inspectContainer = ref('app')
const images = ref<Record<string, string>>({})
const saving = ref(false)
const message = ref('')
const services = computed(() => [...new Map(containers.value.map(container => [container.service ?? container.name, { name: container.service ?? container.name, image: container.image }])).values()])
const imageChanges = computed(() => Object.fromEntries(services.value.filter(service => images.value[service.name]?.trim() && images.value[service.name]!.trim() !== service.image).map(service => [service.name, images.value[service.name]!.trim()])))
const containerOptions = computed(() => [{ value: 'all', label: 'All containers' }, ...containers.value.map(container => ({ value: container.id, label: container.service ?? container.name }))])
const inspectValue = computed(() => inspectContainer.value !== 'app' ? containers.value.find(container => container.id === inspectContainer.value) : inspected.value)
let active = true
let containerGeneration = 0
let tabGeneration = 0
let timer: ReturnType<typeof setInterval> | undefined
watch(() => props.initialTab, value => { tab.value = value })
watch(() => props.stack, async () => {
  const generation = ++containerGeneration
  loading.value = true; error.value = ''
  try {
    const value = await props.api.listContainers(props.stack.id)
    if (active && generation === containerGeneration) {
      containers.value = value
      for (const container of value) images.value[container.service ?? container.name] ??= container.image
    }
  } catch (cause) { if (active && generation === containerGeneration) error.value = cause instanceof Error ? cause.message : 'Containers unavailable' }
  finally { if (active && generation === containerGeneration) loading.value = false }
  if (props.stack.replica && ['edit', 'upgrade'].includes(tab.value)) tab.value = 'containers'
}, { immediate: true })
async function loadTab() {
  const generation = ++tabGeneration
  tabError.value = ''; message.value = ''; tabLoading.value = true
  try {
    if (tab.value === 'logs') {
      const value = logContainer.value !== 'all' ? await props.api.getContainerLogs(logContainer.value, { tail: 200 }) : await props.api.getStackLogs(props.stack.id, { tail: 200 })
      if (active && generation === tabGeneration) logs.value = value
    } else if (tab.value === 'metrics') {
      const value = await props.api.getStackStats(props.stack.id, range.value)
      if (active && generation === tabGeneration) metrics.value = value
    } else if (['compose', 'edit'].includes(tab.value) && compose.value === undefined) {
      const value = await props.api.getStackConfig(props.stack.id)
      if (active && generation === tabGeneration) { compose.value = value; draft.value = value }
    } else if (tab.value === 'inspect') {
      const value = await props.api.getStack(props.stack.id)
      if (active && generation === tabGeneration) inspected.value = value
    }
  } catch (cause) { if (active && generation === tabGeneration) tabError.value = cause instanceof Error ? cause.message : 'Information unavailable' }
  finally { if (active && generation === tabGeneration) tabLoading.value = false }
}
watch([tab, range, logContainer], () => { void loadTab() }, { immediate: true })
async function save(kind: 'compose' | 'images') {
  if (saving.value || props.stack.replica) return
  if (kind === 'compose' && (!draft.value.trim() || draft.value === compose.value)) return
  if (kind === 'images' && !Object.keys(imageChanges.value).length) return
  saving.value = true; tabError.value = ''; message.value = ''
  try {
    if (kind === 'compose') {
      const value = draft.value
      await props.api.validateCompose({ stack_id: props.stack.id, compose_file: value })
      await props.api.updateCompose(props.stack.id, value, idempotencyKey())
      if (active) { compose.value = value; message.value = 'Compose saved.' }
    } else {
      await props.api.upgradeStack(props.stack.id, imageChanges.value, idempotencyKey())
      if (active) message.value = 'Image upgrade accepted.'
    }
    if (active) emit('updated')
  } catch (cause) { if (active) tabError.value = cause instanceof Error ? cause.message : 'Could not save changes' }
  finally { if (active) saving.value = false }
}
function showContainer(id: string, target: 'logs' | 'inspect') {
  if (target === 'logs') logContainer.value = id
  else inspectContainer.value = id
  tab.value = target
}
function appAction(action: string) {
  if (action === 'logs') { logContainer.value = 'all'; tab.value = 'logs' }
  else emit('action', action)
}
onMounted(() => { timer = setInterval(() => { if (document.visibilityState === 'visible' && ['logs', 'metrics'].includes(tab.value) && !tabLoading.value) void loadTab() }, 5_000) })
onBeforeUnmount(() => { active = false; clearInterval(timer); containerGeneration++; tabGeneration++ })
const containerLabels = { running: 'Running', stopped: 'Stopped', starting: 'Starting', paused: 'Paused', error: 'Error' }
</script>
<template>
  <DetailPanel :title="stack.name" @close="emit('close')">
    <p role="status">{{ stack.state }}</p>
    <div class="workload-plates" aria-label="App state and protection"><AppPlateActions :app="item" @action="appAction" /></div>
    <p v-if="stack.replica">Cached peer view. Make changes on the app's primary node.</p>
    <UiTabs v-model="tab" :tabs="tabs" label="App detail">
      <p v-if="tabError" role="alert">{{ tabError }} <UiButton v-if="!['edit', 'upgrade'].includes(tab)" variant="plain" @click="loadTab">Retry</UiButton></p>
      <p v-if="message" role="status">{{ message }}</p>
      <template v-if="tab === 'containers'">
        <p v-if="error" role="alert">{{ error }}</p>
        <p v-else-if="loading">Loading containers…</p>
        <p v-else-if="!containers.length">No containers</p>
        <section v-for="container in containers" :key="container.id" class="container-detail" :aria-label="container.name">
          <h3>{{ container.service ?? container.name }}</h3>
          <p>{{ container.image }} · {{ container.status }}<template v-if="container.health"> · {{ container.health }}</template></p>
          <div class="workload-actions">
            <UiButton variant="surface" @click="showContainer(container.id, 'logs')">View logs</UiButton>
            <UiButton variant="surface" @click="showContainer(container.id, 'inspect')">Inspect</UiButton>
            <template v-if="!stack.replica">
              <UiDropdown :label="`${container.name} state`" :heading="container.service ?? container.name" :summary="container.status" :items="[{ id: 'start', label: 'Start container', disabled: ['running', 'starting', 'paused'].includes(container.status) }, { id: 'stop', label: 'Stop container', disabled: !['running', 'starting', 'paused'].includes(container.status) }]" @select="name => emit('command', [name, stack.id, container.id])">
                <template #trigger="{ toggle, open, controls }"><UiPlateCard :label="containerLabels[container.status]" :busy="container.status === 'starting'" :tone="container.status === 'error' ? 'error' : 'default'" :active="container.status === 'running'" :disabled="app?.actionPending" :aria-label="`${container.name} state actions`" aria-haspopup="menu" :aria-expanded="open" :aria-controls="controls" @click="toggle" /></template>
              </UiDropdown>
              <UiButton variant="surface" @click="emit('command', ['exec', stack.id, container.id, '--'], false)">Run command…</UiButton>
            </template>
            <UiPlateCard v-else :label="containerLabels[container.status]" :active="container.status === 'running'" disabled />
          </div>
        </section>
      </template>
      <template v-else-if="tab === 'logs'">
        <div class="tab-controls"><UiSelect v-model="logContainer" :options="containerOptions" aria-label="Log source" /><UiButton variant="surface" :disabled="tabLoading" @click="loadTab">Refresh logs</UiButton></div>
        <pre v-if="logs.length" class="log-output">{{ logs.map(line => `${line.ts} ${line.stream}: ${line.msg}`).join('\n') }}</pre>
        <p v-else-if="!tabError">{{ tabLoading ? 'Loading logs…' : 'No recent logs' }}</p>
      </template>
      <template v-else-if="tab === 'metrics'">
        <div class="tab-controls"><UiSelect v-model="range" :options="['1h', '6h', '24h', '7d'].map(value => ({ value, label: value }))" aria-label="Metrics range" required /><UiButton variant="surface" :disabled="tabLoading" @click="loadTab">Refresh metrics</UiButton></div>
        <dl v-if="metrics.length" class="metric-values"><template v-for="series in metrics" :key="series.label"><dt>{{ series.label }}</dt><dd>{{ series.points.at(-1)?.value ?? '—' }} {{ series.unit }}</dd></template></dl>
        <p v-else-if="!tabError">{{ tabLoading ? 'Loading metrics…' : 'No metrics available' }}</p>
      </template>
      <template v-else-if="tab === 'compose'">
        <pre v-if="compose !== undefined">{{ compose || 'No Compose configuration' }}</pre><p v-else-if="!tabError">Loading Compose…</p>
      </template>
      <template v-else-if="tab === 'inspect'">
        <UiSelect v-model="inspectContainer" :options="[{ value: 'app', label: 'App' }, ...containerOptions.slice(1)]" aria-label="Inspect target" />
        <pre v-if="inspectValue">{{ JSON.stringify(inspectValue, null, 2) }}</pre><p v-else-if="!tabError">Loading inspection…</p>
      </template>
      <form v-else-if="tab === 'edit' && !stack.replica" @submit.prevent="save('compose')">
        <label for="app-compose-editor">Compose configuration</label><UiTextarea id="app-compose-editor" v-model="draft" aria-label="Compose configuration" spellcheck="false" :disabled="saving || compose === undefined" />
        <p>Validate and save the app's Compose configuration.</p><UiButton variant="surface" type="submit" :disabled="saving || compose === undefined || !draft.trim() || draft === compose">{{ saving ? 'Saving…' : 'Save Compose' }}</UiButton>
        <UiButton v-if="compose === undefined && !tabLoading" variant="plain" @click="loadTab">Retry loading Compose</UiButton>
      </form>
      <form v-else-if="tab === 'upgrade' && !stack.replica" @submit.prevent="save('images')">
        <p>Set replacement images for the services you want to upgrade.</p><p v-if="error" role="alert">{{ error }}</p><p v-else-if="!services.length">{{ loading ? 'Loading images…' : 'No container images available' }}</p>
        <label v-for="service in services" :key="service.name" class="image-field">{{ service.name }}<UiInput v-model="images[service.name]" :aria-label="`Image for ${service.name}`" variant="field" required :disabled="saving" /></label>
        <UiButton variant="surface" type="submit" :disabled="saving || !Object.keys(imageChanges).length">{{ saving ? 'Upgrading…' : 'Upgrade images' }}</UiButton>
      </form>
    </UiTabs>
  </DetailPanel>
</template>
<style scoped>
.workload-plates, .workload-actions, .tab-controls { display: flex; flex-wrap: wrap; gap: 8px; }
.workload-plates, .tab-controls { margin-bottom: 16px; }
.container-detail { border-top: 1px solid var(--line); padding: 12px 0; overflow-wrap: anywhere; }
pre { max-height: 480px; overflow: auto; padding: 12px; background: var(--surface-canvas); font-size: var(--text-meta); white-space: pre-wrap; overflow-wrap: anywhere; }
.metric-values { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 12px; }
dd { margin: 0; }
form { display: grid; gap: 12px; }
form .ui-textarea { min-height: 320px; width: 100%; }
.image-field { display: grid; gap: 6px; }
p { overflow-wrap: anywhere; }
[role="alert"] { color: var(--status-error); }
</style>
