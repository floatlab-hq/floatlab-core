<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { IconArrowBigRightLine, IconHistory, IconPencil, IconPlayerPlayFilled, IconPlayerStopFilled, IconAlertTriangleFilled } from '@tabler/icons-vue'
import UiButton from './UiButton.vue'
import { nodeCode } from '../dashboard/node-code'

const props = withDefaults(defineProps<{
  label: string
  kind?: 'runtime' | 'replication' | 'backup'
  host?: string
  hostHealth?: string
  count?: number
  active?: boolean
  busy?: boolean
  tone?: 'default' | 'warning' | 'error'
  progress?: number
  completedAt?: string
  detail?: string
  disabled?: boolean
  motion?: 'start' | 'stop' | 'restart'
}>(), { kind: 'runtime', tone: 'default', host: '', hostHealth: 'unknown', count: 0, progress: undefined, completedAt: undefined, detail: '', motion: undefined })
const transition = ref('')
const now = ref(Date.now())
let ageTimer: ReturnType<typeof setInterval> | undefined
watch(() => [props.kind, props.label, props.completedAt] as const, ([kind, label, value]) => {
  clearInterval(ageTimer)
  now.value = Date.now()
  if (kind === 'backup' && label === 'Synced' && value && Number.isFinite(Date.parse(value))) ageTimer = setInterval(() => { now.value = Date.now() }, 60_000)
}, { immediate: true })
onBeforeUnmount(() => clearInterval(ageTimer))
const backupAge = computed(() => {
  if (props.kind !== 'backup' || props.label !== 'Synced' || !props.completedAt) return ''
  const elapsed = now.value - Date.parse(props.completedAt)
  if (!Number.isFinite(elapsed)) return ''
  const minutes = Math.max(0, Math.floor(elapsed / 60_000))
  return minutes < 1 ? 'just now' : minutes < 60 ? `${minutes}m ago` : minutes < 1440 ? `${Math.floor(minutes / 60)}h ago` : `${Math.floor(minutes / 1440)}d ago`
})
const displayLabel = computed(() => props.kind === 'backup' && props.label === 'Synced' ? 'Backed up' : props.label)
const accessibleLabel = computed(() => [displayLabel.value, backupAge.value, props.host].filter(Boolean).join(' · '))
const motion = computed(() => props.motion || (props.label === 'Starting' ? 'start' : props.label === 'Stopping' ? 'stop' : props.label === 'Restarting' ? 'restart' : transition.value))
const percent = computed(() => props.progress === undefined || !Number.isFinite(props.progress) ? undefined : Math.max(0, Math.min(100, props.progress)))
watch(() => [props.active, props.label] as const, ([active, label], [previous, previousLabel]) => {
  if (label === 'Starting') transition.value = 'start'
  else if (label === 'Stopping') transition.value = 'stop'
  else if (label === 'Restarting') transition.value = 'restart'
  else if (['Starting', 'Stopping', 'Restarting'].includes(previousLabel)) transition.value = ''
  else if (active !== previous) transition.value = active ? 'start' : 'stop'
})
const icon = computed(() => props.tone === 'error' ? IconAlertTriangleFilled : props.kind === 'replication' ? IconArrowBigRightLine : props.kind === 'backup' ? IconHistory : props.active ? IconPlayerPlayFilled : IconPlayerStopFilled)
</script>

<template>
  <UiButton class="plate-card" :class="[kind, tone, { 'is-active': active, 'is-busy': busy }, motion && `water-${motion}`]" :disabled="disabled" :aria-busy="busy || undefined" :title="[detail, completedAt].filter(Boolean).join(' · ')" :aria-label="accessibleLabel" @animationend.self="transition = ''">
    <span class="plate-status">
      <span v-if="kind === 'runtime'" class="runtime-liquid" aria-hidden="true" @animationend="transition = ''"><svg viewBox="0 0 200 12" preserveAspectRatio="none"><path d="M0 6 Q25 0 50 6 T100 6 T150 6 T200 6 V12 H0Z" /></svg></span>
      <span v-if="busy && kind !== 'runtime'" class="plate-progress" :class="{ indeterminate: percent === undefined }" :style="percent === undefined ? undefined : { transform: `scaleX(${percent / 100})` }" role="progressbar" :aria-label="`${label} progress`" :aria-valuenow="percent" :aria-valuemin="0" :aria-valuemax="100" :aria-valuetext="percent === undefined ? detail || label : `${percent}%`" />
      <span class="plate-caption"><component :is="icon" class="plate-state-icon" :size="11" aria-hidden="true" /><IconPencil class="plate-edit-icon" :size="11" aria-hidden="true" /><span>{{ displayLabel }}</span><time v-if="backupAge" :datetime="completedAt">· {{ backupAge }}</time><span v-if="busy && percent !== undefined" class="plate-percent">{{ Math.round(percent) }}%</span></span>
      <span v-if="kind === 'runtime'" class="plate-caption runtime-ink" aria-hidden="true"><component :is="icon" class="plate-state-icon" :size="11" /><IconPencil class="plate-edit-icon" :size="11" /><span>{{ displayLabel }}</span><time v-if="backupAge" :datetime="completedAt">· {{ backupAge }}</time></span>
    </span>
    <span v-if="host" class="plate-host" :class="`host-${hostHealth || 'unknown'}`" :aria-label="`${host} · ${hostHealth || 'unknown'}`">{{ nodeCode(host) }}<span v-if="count > 1">+{{ count - 1 }}</span></span>
  </UiButton>
</template>

<style scoped>
.plate-card { display: inline-flex; align-items: stretch; isolation: isolate; min-height: 32px; height: 32px; padding: 3px; border: 0; border-radius: 2px; background: var(--plate-surface, var(--surface-recessed)); color: var(--text-secondary); font-size: 10px; font-style: normal; font-weight: 500; line-height: 18px; white-space: nowrap; --water: var(--omarchy-accent); --runtime-duration: 2400ms; }
.plate-status { position: relative; display: grid; flex: 1; min-width: 64px; min-height: 26px; overflow: hidden; isolation: isolate; }
.plate-caption { grid-area: 1 / 1; display: flex; align-items: center; justify-content: center; gap: 4px; position: relative; z-index: 1; padding: 0 6px; }
.plate-edit-icon { display: none; }
.plate-card:enabled:hover .plate-edit-icon { display: block; }
.plate-card:enabled:hover .plate-state-icon { display: none; }
.plate-host { display: inline-flex; align-items: center; gap: 4px; margin-left: 2px; padding: 0 6px; border: 0; border-radius: 1px; background: var(--surface-canvas); color: var(--text-secondary); font-weight: 700; }
.host-offline { border: 1px dotted var(--line); opacity: .7; }
.warning { color: var(--status-warning); }
.error { color: var(--status-error); --water: var(--status-error); }
.plate-card:disabled { cursor: default; }
.plate-card.runtime.is-active { background: var(--water); }
.runtime .plate-status { min-width: 64px; background: var(--plate-surface, var(--surface-recessed)); }
.runtime-liquid { position: absolute; inset: -2px; z-index: 0; background: var(--water); transform: translateY(140%); pointer-events: none; }
.runtime-liquid svg { position: absolute; width: 200%; height: 8px; left: 0; top: -7px; fill: var(--water); }
.runtime-ink { color: var(--surface-recessed); clip-path: inset(100% 0 0); }
.is-active .runtime-liquid { transform: translateY(0); }
.is-active .runtime-ink { clip-path: inset(0); }
.water-start .runtime-liquid { animation: water-fill var(--runtime-duration) both; }
.water-start .runtime-ink { animation: ink-fill var(--runtime-duration) both; }
.water-stop .runtime-liquid { animation: water-drain var(--runtime-duration) both; }
.water-stop .runtime-ink { animation: ink-drain var(--runtime-duration) both; }
.water-restart { --runtime-duration: 3200ms; }
.water-restart .runtime-liquid { animation: water-restart var(--runtime-duration) both; }
.water-restart .runtime-ink { animation: ink-restart var(--runtime-duration) both; }
:is(.water-start,.water-stop,.water-restart) .runtime-liquid svg { animation: water-slosh 560ms ease-in-out infinite alternate; }
.plate-progress { position: absolute; inset: 0; transform-origin: left; background: currentColor; opacity: .13; transition: transform 300ms linear; pointer-events: none; }
.plate-progress.indeterminate { width: 40%; animation: plate-sync 1600ms ease-in-out infinite alternate; }
.plate-percent { font-size: 9px; font-variant-numeric: tabular-nums; }
@keyframes water-fill { 0% { transform: translateY(140%); } 18% { transform: translateY(82%); } 66% { transform: translateY(35%); } 82% { transform: translateY(16%); } 90% { transform: translateY(-5%); } 96%,100% { transform: translateY(0); } }
@keyframes water-drain { 0% { transform: translateY(0); } 18% { transform: translateY(12%); } 72% { transform: translateY(80%); } 92%,100% { transform: translateY(140%); } }
@keyframes water-restart { 0% { transform: translateY(0); } 32%,40% { transform: translateY(140%); } 60% { transform: translateY(65%); } 82% { transform: translateY(16%); } 90% { transform: translateY(-5%); } 96%,100% { transform: translateY(0); } }
@keyframes ink-fill { 0% { clip-path: inset(100% 0 0); } 18% { clip-path: inset(82% 0 0); } 66% { clip-path: inset(35% 0 0); } 82% { clip-path: inset(16% 0 0); } 90%,100% { clip-path: inset(0); } }
@keyframes ink-drain { 0% { clip-path: inset(0); } 18% { clip-path: inset(12% 0 0); } 72% { clip-path: inset(80% 0 0); } 92%,100% { clip-path: inset(100% 0 0); } }
@keyframes ink-restart { 0% { clip-path: inset(0); } 32%,40% { clip-path: inset(100% 0 0); } 60% { clip-path: inset(65% 0 0); } 82% { clip-path: inset(16% 0 0); } 90%,100% { clip-path: inset(0); } }
@keyframes water-slosh { from { transform: translateX(0) scaleY(.65); } to { transform: translateX(-50%) scaleY(1.3); } }
@keyframes plate-sync { from { transform: translateX(0); } to { transform: translateX(150%); } }
@media (prefers-reduced-motion: reduce) { .plate-card * { animation: none !important; transition: none !important; } .plate-progress.indeterminate { width: 100%; opacity: .08; } }
</style>
