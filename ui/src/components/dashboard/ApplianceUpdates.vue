<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { UpdateAction, UpdateApi, UpdateComponent, UpdateRelease, UpdateStatus } from '@floatlab/client'
import UiButton from '../UiButton.vue'
import UiSelect from '../UiSelect.vue'
const props = defineProps<{ api: UpdateApi; nodeId: string }>()
const component = ref<UpdateComponent>('management')
const releases = ref<UpdateRelease[]>([])
const release = ref('')
const status = ref<UpdateStatus | null>(null)
const busy = ref(false)
const error = ref('')
const pending = ref<UpdateAction>()
let generation = 0
let active = true
let timer: ReturnType<typeof setInterval> | undefined
let retry: { signature: string; key: string } | undefined
async function refresh(discover = false) {
  if (busy.value) return
  const request = ++generation
  busy.value = true
  try {
    const current = await props.api.getUpdateStatus(props.nodeId, component.value)
    if (!active || request !== generation) return
    status.value = current
    if (discover) {
      const available = await props.api.listUpdateReleases(props.nodeId, component.value)
      if (!active || request !== generation) return
      releases.value = available
    }
    error.value = ''
  } catch (cause) {
    if (active && request === generation) error.value = cause instanceof Error ? cause.message : 'Update status unavailable'
  } finally { if (active && request === generation) busy.value = false }
}
async function run() {
  const action = pending.value
  if (!action || busy.value) return
  const initial = action === 'start' || action === 'prepare'
  const request = initial ? { release: release.value } : { operation_id: status.value?.operation_id }
  if (initial ? !request.release : !request.operation_id) return
  const signature = JSON.stringify([props.nodeId, component.value, action, request])
  if (retry?.signature !== signature) retry = { signature, key: globalThis.crypto.randomUUID() }
  const version = ++generation
  busy.value = true; error.value = ''
  try {
    const result = await props.api.runUpdateAction(props.nodeId, component.value, action, request, retry.key)
    if (!active || version !== generation) return
    status.value = result; pending.value = undefined; retry = undefined
  } catch (cause) {
    if (active && version === generation) error.value = cause instanceof Error ? cause.message : 'Update action failed'
  } finally { if (active && version === generation) busy.value = false }
}
watch(component, () => { generation++; busy.value = false; release.value = ''; releases.value = []; status.value = null; pending.value = undefined; retry = undefined; void refresh(true) })
onMounted(() => { void refresh(true); timer = setInterval(() => { void refresh() }, 5000) })
onBeforeUnmount(() => { active = false; generation++; clearInterval(timer) })
</script>
<template>
  <section aria-label="Appliance updates" class="appliance-updates">
    <h2>Appliance updates</h2>
    <label>Component <UiSelect v-model="component" :disabled="busy" :options="[{ value: 'management', label: 'Management stack' }, { value: 'host', label: 'Host OS' }]" /></label>
    <label>Release <UiSelect v-model="release" :disabled="busy" :options="releases.map(item => ({ value: item.tag_name, label: item.tag_name + (item.prerelease ? ' (prerelease)' : '') }))" /></label>
    <div class="actions">
      <UiButton :disabled="busy || !release" @click="pending = component === 'host' ? 'prepare' : 'start'">{{ component === 'host' ? 'Stage OS image' : 'Start management update' }}</UiButton>
      <UiButton :disabled="busy" @click="refresh(true)">Refresh updates</UiButton>
      <template v-if="status?.operation_id">
        <UiButton v-if="component === 'host'" :disabled="busy" @click="pending = 'activate'">Reboot into trial</UiButton>
        <UiButton v-if="component === 'host'" :disabled="busy" @click="pending = 'commit'">Commit trial</UiButton>
        <UiButton :disabled="busy" @click="pending = 'rollback'">Roll back</UiButton>
        <UiButton :disabled="busy" @click="pending = 'cleanup'">Clean up old versions</UiButton>
      </template>
    </div>
    <div v-if="pending" role="group" aria-label="Confirm update action">
      <p>Confirm {{ pending }}: {{ component }} {{ pending === 'start' || pending === 'prepare' ? release : status?.operation_id }}.</p>
      <p v-if="pending === 'activate'">This reboots the appliance and interrupts its workloads. Commit the trial explicitly after checking it.</p>
      <p v-if="pending === 'rollback'">Rollback discards management changes since the update checkpoint. Changes to workloads are not undone.</p>
      <UiButton :disabled="busy" @click="run">Confirm {{ pending }}</UiButton>
      <UiButton :disabled="busy" @click="pending = undefined">Cancel</UiButton>
    </div>
    <p role="status">{{ busy ? 'Checking updates…' : status ? `${status.state} · ${status.phase}` : 'No update transaction' }}</p>
    <dl v-if="status?.operation_id"><dt>Transaction</dt><dd>{{ status.operation_id }}</dd><dt>Previous release</dt><dd>{{ status.source_release || '—' }}</dd><dt>Target</dt><dd>{{ status.target_release || '—' }}</dd><template v-if="component === 'host'"><dt>Hard-hang recovery</dt><dd>{{ status.watchdog ? 'Active watchdog; hardware reset behavior is unverified' : 'Not verified; a kernel hang may require a physical reset' }}</dd></template></dl>
    <p v-if="status?.error" role="alert">{{ status.error }}</p>
    <p v-if="status?.rollback_error" role="alert">Recovery failed: {{ status.rollback_error }}</p>
    <div v-if="error" role="alert"><p>{{ error }}</p><p>Status reconnects automatically. Hostd continues accepted updates while the management API is unavailable.</p></div>
  </section>
</template>
<style scoped>
.appliance-updates { display: grid; gap: 12px; margin-top: 20px; }
label, .actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
dl { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 8px; overflow-wrap: anywhere; }
dd { margin: 0; }
</style>
