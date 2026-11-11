import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LiveDashboard from './LiveDashboard.vue'
import { fixtureApi, interactiveFixtureApi, stack, node } from '../../dashboard/live-fixtures'
import { userEvent, within } from 'storybook/test'
import type { Operation } from '@floatlab/client'
const args = { api: interactiveFixtureApi() }
const meta: Meta<typeof LiveDashboard> = { title: '2. Pattern/Live Dashboard', component: LiveDashboard, args, parameters: { layout: 'fullscreen', docs: { description: { component: 'Routine lifecycle, sync, snapshot, and backup actions execute directly from their controls. Requests disable duplicate actions while pending, errors appear inline, and the command palette draft is preserved.' } } }, render: args => ({ components: { LiveDashboard }, setup: () => ({ args }), template: '<LiveDashboard v-bind="args" />' }) }
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
    await userEvent.click(await within(canvasElement).findByRole('button', { name: 'Open plex' }))
  },
}
const moveHost = { ...node, id: 'node-2', name: 'lab-beta' }
function moveFixtureApi() {
  const published = { ...stack, compose_file: 'services: { web: { ports: ["8080:80"] } }' }
  const api = interactiveFixtureApi([published])
  let moved = false
  let polls = 0
  return {
    ...api,
    listNodes: async () => [node, moveHost],
    listStacks: async () => [moved ? { ...published, primary_node: moveHost.id, secondary_node: node.id } : published],
    listNetworkPools: async () => [],
    getNodeStats: async (id: string) => ({ node_id: id, range: '1h' as const, series: {} }),
    getStorageStats: async (id: string) => ({ node_id: id, range: '1h' as const, series: {} }),
    request: async (...args: Parameters<typeof api.request>) => args[0] === '/stacks/stack-1/status' ? { stack_ip: '192.168.1.50' } : api.request(...args),
    moveStack: async () => { polls = 0; return { operation_id: 'move-example' } },
    getOperation: async (): Promise<Operation> => {
      polls++
      moved = polls >= 4
      const checkpoint = ['seeding', 'reserving-destination', 'final-sync'][polls - 1] ?? 'succeeded'
      return { operation_id: 'move-example', action: 'move', actor: 'admin', state: moved ? 'succeeded' : 'running', checkpoint, created_at: node.created_at, updated_at: node.updated_at }
    },
  }
}
export const MoveApp: StoryObj<typeof meta> = {
  args: { api: moveFixtureApi() },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(await canvas.findByRole('button', { name: /^plex runtime:/ }))
    await userEvent.click(await within(canvasElement.ownerDocument.body).findByRole('menuitem', { name: 'Move app…' }))
    await userEvent.selectOptions(await canvas.findByLabelText('Destination host'), moveHost.id)
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
    await userEvent.click(await within(canvasElement).findByRole('button', { name: /^Events:/ }))
  },
}
export const CachedPeerView: StoryObj<typeof meta> = { args: { api: { ...fixtureApi(), listStacks: async () => [stack, { ...stack, id: 'peer-stack', primary_node: 'peer-node', name: 'web', replica: true, stale: true, observed_revision: 42 }] } } }
const devApi = fixtureApi()
const raftHealth = devApi.getRaftHealth
devApi.getRaftHealth = async () => ({ ...await raftHealth(), dev_mode: true })
export const Development: StoryObj<typeof meta> = { args: { api: devApi } }
export const ApplianceUpdates: StoryObj<typeof meta> = { args: { api: { ...fixtureApi(), listUpdateReleases: async () => [{ tag_name: 'manage-v1.2.3' }], getUpdateStatus: async () => null, runUpdateAction: async () => ({ operation_id: 'example', state: 'running', phase: 'preflight' }) } } }

export const ActiveProtection: StoryObj<typeof meta> = {
  args: { api: { ...interactiveFixtureApi([{ ...stack, secondary_node: 'node-2' }]),
    listNodes: async () => [{ ...node, name: 'node-01' }, { ...node, id: 'node-2', name: 'node-02' }, { ...node, id: 'node-3', name: 'node-03' }],
    getNodeStats: async id => ({ node_id: id, range: '1h', series: {} }),
    getStorageStats: async id => ({ node_id: id, range: '1h', series: {} }),
    listBackupTargets: async () => [{ stack_id: stack.id, dataset: 'floatlab/plex/data', node_id: 'node-3', source_node_id: node.id, enabled: true, cleanup_pending: false, schedule: 'h/24', local_policy: 'h/24' }],
    listReplication: async () => [{ id: 'standby-sync', dest_node_id: 'node-2', state: 'running', created_at: new Date().toISOString() }, { id: 'backup-sync', dest_node_id: 'node-3', dataset: 'floatlab/plex/data', state: 'running', created_at: new Date().toISOString() }],
  } },
}
