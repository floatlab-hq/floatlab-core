import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LiveDashboard from './LiveDashboard.vue'
import { fixtureApi, interactiveFixtureApi, stack } from '../../dashboard/live-fixtures'
import { userEvent, within } from 'storybook/test'
const args = { api: interactiveFixtureApi() }
const meta: Meta<typeof LiveDashboard> = { title: '2. Pattern/Live Dashboard', component: LiveDashboard, args, parameters: { layout: 'fullscreen' }, render: args => ({ components: { LiveDashboard }, setup: () => ({ args }), template: '<LiveDashboard v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Activity: StoryObj<typeof meta> = {
  args: { api: { ...interactiveFixtureApi(), listNotifications: async () => [], listAuditLog: async () => [
    { id: 'audit-1', actor: 'admin', action: 'StartStack', resource_type: 'stack', resource_id: stack.id, ts: '2026-11-02T05:00:00Z', outcome: 'failed' },
    { id: 'audit-2', actor: 'admin', action: 'CreateStack', resource_type: 'stack', resource_id: stack.id, ts: '2026-11-02T04:59:00Z', outcome: 'succeeded' },
  ] } },
}
export const PaletteInContext: StoryObj<typeof meta> = {
  args: { api: interactiveFixtureApi() },
  play: async ({ canvasElement }) => {
    await userEvent.click(await within(canvasElement).findByRole('button', { name: 'Open command palette' }))
  },
}
export const Empty: StoryObj<typeof meta> = { args: { api: interactiveFixtureApi([]) } }
export const Running: StoryObj<typeof meta> = { args: { api: interactiveFixtureApi([stack]) } }
export const AddApp: StoryObj<typeof meta> = {
  args: { api: interactiveFixtureApi([]) },
  play: async ({ canvasElement }) => {
    await userEvent.click(await within(canvasElement).findByRole('button', { name: 'App' }))
  },
}
export const WorkloadActions: StoryObj<typeof meta> = {
  args: { api: interactiveFixtureApi([stack]) },
  play: async ({ canvasElement }) => {
    await userEvent.click(await within(canvasElement).findByRole('button', { name: 'Tools for plex' }))
  },
}
export const NodeKey: StoryObj<typeof meta> = {
  args: { api: interactiveFixtureApi() },
  play: async ({ canvasElement }) => {
    await userEvent.click(await within(canvasElement).findByRole('button', { name: /Node key/ }))
  },
}
export const EventHistory: StoryObj<typeof meta> = {
  args: { api: { ...interactiveFixtureApi(), listAuditLog: async () => Array.from({ length: 5 }, (_, index) => ({ id: `audit-${index}`, actor: 'admin', action: 'Snapshot', resource_type: 'stack', resource_id: stack.id, ts: `2026-11-02T05:0${index}:00Z`, outcome: 'succeeded' })) } },
  play: async ({ canvasElement }) => {
    await userEvent.click(await within(canvasElement).findByRole('button', { name: /View all .* events/ }))
  },
}
export const CachedPeerView: StoryObj<typeof meta> = { args: { api: { ...fixtureApi(), listStacks: async () => [stack, { ...stack, id: 'peer-stack', primary_node: 'peer-node', name: 'web', replica: true, stale: true, observed_revision: 42 }] } } }
const devApi = fixtureApi()
const raftHealth = devApi.getRaftHealth
devApi.getRaftHealth = async () => ({ ...await raftHealth(), dev_mode: true })
export const Development: StoryObj<typeof meta> = { args: { api: devApi } }
export const ApplianceUpdates: StoryObj<typeof meta> = { args: { api: { ...fixtureApi(), listUpdateReleases: async () => [{ tag_name: 'manage-v1.2.3' }], getUpdateStatus: async () => null, runUpdateAction: async () => ({ operation_id: 'example', state: 'running', phase: 'preflight' }) } } }
