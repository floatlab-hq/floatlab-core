import type { Meta, StoryObj } from '@storybook/vue3-vite'
import WorkloadDetail from './WorkloadDetail.vue'
import { interactiveFixtureApi, stack } from '../../dashboard/live-fixtures'
const meta: Meta<typeof WorkloadDetail> = {
  title: '2. Pattern/Workload Detail', component: WorkloadDetail,
  args: { stack, api: interactiveFixtureApi([stack]) },
  argTypes: { onCommand: { action: 'command' }, onClose: { action: 'close' } },
}
export default meta
export const Running: StoryObj<typeof meta> = {}
export const Stopped: StoryObj<typeof meta> = { args: { stack: { ...stack, state: 'Idle' } } }
export const Provisioning: StoryObj<typeof meta> = { args: { stack: { ...stack, state: 'Provisioning' } } }
export const Failed: StoryObj<typeof meta> = { args: { stack: { ...stack, state: 'Failed' } } }
export const CachedPeer: StoryObj<typeof meta> = { args: { stack: { ...stack, replica: true } } }
export const Unavailable: StoryObj<typeof meta> = { args: { api: { ...interactiveFixtureApi(), listContainers: async () => { throw new Error('Containers unavailable') } } } }
