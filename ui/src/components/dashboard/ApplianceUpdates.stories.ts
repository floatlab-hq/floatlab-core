import type { Meta, StoryObj } from '@storybook/vue3-vite'
import type { UpdateApi } from '@floatlab/client'
import ApplianceUpdates from './ApplianceUpdates.vue'
const status = { operation_id: 'example', state: 'ready', phase: 'staged', target_release: 'host-v1.2.3' }
const api: UpdateApi = {
  listUpdateReleases: async (_node, component) => [{ tag_name: `${component === 'host' ? 'host' : 'manage'}-v1.2.3` }],
  getUpdateStatus: async () => status,
  runUpdateAction: async () => status,
}
const meta: Meta<typeof ApplianceUpdates> = { title: '2. Pattern/Appliance Updates', component: ApplianceUpdates, args: { api, nodeId: 'node' }, render: args => ({ components: { ApplianceUpdates }, setup: () => ({ args }), template: '<ApplianceUpdates v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Disconnected: StoryObj<typeof meta> = { args: { api: { ...api, getUpdateStatus: async () => { throw new Error('Management is restarting') } } } }
export const RecoveryFailed: StoryObj<typeof meta> = { args: { api: { ...api, getUpdateStatus: async () => ({ ...status, state: 'recovery_failed', phase: 'restoring', rollback_error: 'Dataset is busy' }) } } }
