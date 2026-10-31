import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LiveDashboard from './LiveDashboard.vue'
import { fixtureApi, stack } from '../../dashboard/live-fixtures'
const args = { api: fixtureApi() }
const meta: Meta<typeof LiveDashboard> = { title: '2. Pattern/Live Dashboard', component: LiveDashboard, args, render: args => ({ components: { LiveDashboard }, setup: () => ({ args }), template: '<LiveDashboard v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
export const CachedPeerView: StoryObj<typeof meta> = { args: { api: { ...fixtureApi(), listStacks: async () => [stack, { ...stack, id: 'peer-stack', primary_node: 'peer-node', name: 'web', replica: true, stale: true, observed_revision: 42 }] } } }
const devApi = fixtureApi()
const raftHealth = devApi.getRaftHealth
devApi.getRaftHealth = async () => ({ ...await raftHealth(), dev_mode: true })
export const Development: StoryObj<typeof meta> = { args: { api: devApi } }
export const ApplianceUpdates: StoryObj<typeof meta> = { args: { api: { ...fixtureApi(), listUpdateReleases: async () => [{ tag_name: 'manage-v1.2.3' }], getUpdateStatus: async () => null, runUpdateAction: async () => ({ operation_id: 'example', state: 'running', phase: 'preflight' }) } } }
