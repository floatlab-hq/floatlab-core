<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import type { Container, FloatLabApi, Stack } from '@floatlab/client'
import DetailPanel from '../DetailPanel.vue'
import UiButton from '../UiButton.vue'
const props = defineProps<{ stack: Stack; api: FloatLabApi }>()
const emit = defineEmits<{ close: []; command: [args: string[], run?: boolean] }>()
const containers = ref<Container[]>([])
const error = ref('')
const loading = ref(true)
let active = true
watch(() => props.stack, async () => {
  loading.value = true; error.value = ''
  try { const value = await props.api.listContainers(props.stack.id); if (active) containers.value = value }
  catch (cause) { if (active) error.value = cause instanceof Error ? cause.message : 'Containers unavailable' }
  finally { if (active) loading.value = false }
}, { immediate: true })
onBeforeUnmount(() => { active = false })
const commands = [
  { label: 'View logs', command: 'logs' }, { label: 'Metrics', command: 'stats' },
  { label: 'View Compose', command: 'config' }, { label: 'Inspect', command: 'info' },
]
const mutations = [
  { label: 'Edit Compose', command: 'edit' }, { label: 'Upgrade images', command: 'upgrade' },
  { label: 'Delete app', command: 'delete' },
]
</script>
<template>
  <DetailPanel :title="stack.name" @close="emit('close')">
    <p role="status">{{ stack.state }}</p>
    <p v-if="stack.replica">Cached peer view. Make changes on the app's primary node.</p>
    <div class="workload-actions" aria-label="App actions">
      <template v-if="!stack.replica">
        <UiButton v-if="['Idle', 'Failed'].includes(stack.state)" variant="surface" @click="emit('command', ['start', stack.id])">Start</UiButton>
        <UiButton v-if="['RunningPrimary', 'RunningBackup'].includes(stack.state)" variant="surface" @click="emit('command', ['stop', stack.id])">Stop</UiButton>
      </template>
      <UiButton v-for="action in commands" :key="action.command" variant="surface" @click="emit('command', [action.command, stack.id])">{{ action.label }}</UiButton>
      <UiButton variant="surface" @click="emit('command', ['snapshot', 'list', stack.id])">View snapshots</UiButton>
      <template v-if="!stack.replica">
        <UiButton variant="surface" @click="emit('command', ['snapshot', 'create', stack.id])">Create snapshot</UiButton>
        <UiButton v-for="action in mutations" :key="action.command" variant="surface" @click="emit('command', [action.command, stack.id])">{{ action.label }}</UiButton>
      </template>
    </div>
    <h2>Containers</h2>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-else-if="loading">Loading containers…</p>
    <p v-else-if="!containers.length">No containers</p>
    <section v-for="container in containers" :key="container.id" class="container-detail" :aria-label="container.name">
      <h3>{{ container.service ?? container.name }}</h3>
      <p>{{ container.image }} · {{ container.status }}<template v-if="container.health"> · {{ container.health }}</template></p>
      <div class="workload-actions">
        <UiButton variant="surface" @click="emit('command', ['logs', stack.id, container.id])">View logs</UiButton>
        <UiButton variant="surface" @click="emit('command', ['info', stack.id, container.id])">Inspect</UiButton>
        <template v-if="!stack.replica">
          <UiButton variant="surface" @click="emit('command', ['start', stack.id, container.id])">Start container</UiButton>
          <UiButton variant="surface" @click="emit('command', ['stop', stack.id, container.id])">Stop container</UiButton>
          <UiButton variant="surface" @click="emit('command', ['exec', stack.id, container.id, '--'], false)">Run command…</UiButton>
        </template>
      </div>
    </section>
  </DetailPanel>
</template>
<style scoped>
.workload-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.container-detail { border-top: 1px solid var(--line); padding: 12px 0; overflow-wrap: anywhere; }
p { overflow-wrap: anywhere; }
</style>
