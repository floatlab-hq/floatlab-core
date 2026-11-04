import type { Meta, StoryObj } from '@storybook/vue3-vite'
import { ref } from 'vue'
import UiPlateCard from './UiPlateCard.vue'
import UiButton from './UiButton.vue'
const meta: Meta<typeof UiPlateCard> = { title: '1. Primitive/Plate Card', component: UiPlateCard, args: { label: 'Running', active: true, host: 'node-01', hostHealth: 'online' } }
export default meta
export const Running: StoryObj<typeof meta> = {}
export const BackedUp: StoryObj<typeof meta> = { args: { kind: 'backup', label: 'Synced', active: false, completedAt: new Date(Date.now() - 2 * 3600_000).toISOString(), host: 'cedar', count: 3 } }
export const Stopped: StoryObj<typeof meta> = { args: { label: 'Stopped', active: false } }
export const Syncing: StoryObj<typeof meta> = { args: { kind: 'replication', label: 'Syncing', active: false, busy: true, progress: 42, detail: '42% transferred' } }
export const UnknownTotal: StoryObj<typeof meta> = { args: { kind: 'backup', label: 'Syncing', active: false, busy: true, detail: 'Running · total unavailable' } }
export const Failed: StoryObj<typeof meta> = { args: { kind: 'backup', label: 'Failed', active: false, tone: 'error', detail: 'Destination disk full' } }
export const FillDrainRestart: StoryObj<typeof meta> = {
  render: () => ({ components: { UiPlateCard, UiButton }, setup() { const active = ref(false); const motion = ref<'start' | 'stop' | 'restart'>(); const revision = ref(0); function run(value: 'start' | 'stop' | 'restart') { active.value = value !== 'stop'; motion.value = value; revision.value++ } return { active, motion, revision, run } }, template: `<div style="display:grid;gap:16px;justify-items:start"><UiPlateCard :key="revision" :label="active ? 'Running' : 'Stopped'" :active="active" :motion="motion" host="node-01" host-health="online" /><div><UiButton @click="run('start')">Start</UiButton><UiButton @click="run('stop')">Stop</UiButton><UiButton @click="run('restart')">Restart</UiButton></div></div>` }),
}
