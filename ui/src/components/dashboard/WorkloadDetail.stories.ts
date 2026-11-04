import type { Meta, StoryObj } from '@storybook/vue3-vite'
import WorkloadDetail from './WorkloadDetail.vue'
import { interactiveFixtureApi, stack } from '../../dashboard/live-fixtures'
const meta: Meta<typeof WorkloadDetail> = {
  title: '2. Pattern/Workload Detail', component: WorkloadDetail,
  args: { stack, api: interactiveFixtureApi([stack]) },
  parameters: { docs: { description: { component: 'App detail tabs contain live logs, metrics, Compose, inspection, validated Compose editing and image upgrade forms. Container logs and inspection open in these tabs. Cached peer views omit mutation tabs.' } } },
  argTypes: { onAction: { action: 'action' }, onCommand: { action: 'command' }, onClose: { action: 'close' } },
}
export default meta
export const Running: StoryObj<typeof meta> = {}
export const Stopped: StoryObj<typeof meta> = { args: { stack: { ...stack, state: 'Idle' } } }
export const Provisioning: StoryObj<typeof meta> = { args: { stack: { ...stack, state: 'Provisioning' } } }
export const Failed: StoryObj<typeof meta> = { args: { stack: { ...stack, state: 'Failed' } } }
export const CachedPeer: StoryObj<typeof meta> = { args: { stack: { ...stack, replica: true } } }
export const Unavailable: StoryObj<typeof meta> = { args: { api: { ...interactiveFixtureApi(), listContainers: async () => { throw new Error('Containers unavailable') } } } }

export const Logs: StoryObj<typeof meta> = { args: { initialTab: 'logs' } }
export const Metrics: StoryObj<typeof meta> = { args: { initialTab: 'metrics', api: { ...interactiveFixtureApi([stack]), getStackStats: async () => [{ label: 'CPU usage', unit: '%', points: [{ timestamp: 1793750400, value: 12.4 }] }, { label: 'Memory', unit: 'bytes', points: [{ timestamp: 1793750400, value: 134217728 }] }] } } }
export const Compose: StoryObj<typeof meta> = { args: { initialTab: 'compose' } }
export const Inspect: StoryObj<typeof meta> = { args: { initialTab: 'inspect' } }
export const EditCompose: StoryObj<typeof meta> = { args: { initialTab: 'edit' } }
export const UpgradeImages: StoryObj<typeof meta> = { args: { initialTab: 'upgrade' } }
