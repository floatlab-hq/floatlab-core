// Fixtures are consumed only by stories and tests, never the application entrypoint.
import { createApiClient, type Container, type LogLine, type Node, type Stack } from '@floatlab/client'
export const node: Node = {
  id: 'node-1', name: 'lab-alpha', hostname: 'alpha.local', role: 'primary', status: 'online',
  addresses: [{ type: 'LAN-6', address: '2001:db8::1/64', interface: 'eth0' }],
  zfs_pool: 'floatlab', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
}
export const stack: Stack = {
  id: 'stack-1', name: 'plex', state: 'RunningPrimary', primary_node: node.id,
  compose_file: 'services: {}', dataset_path: 'floatlab/plex', failover_mode: 'manual',
  created_at: node.created_at, updated_at: node.updated_at,
}
export const fixtureData: Record<string, unknown> = {
  '/api/v1/nodes': [node], '/api/v1/stacks': [stack],
  '/api/v1/notifications': [{ id: 'event-1', severity: 'info', state: 'unread', title: 'App started', body: 'plex is running', node_id: node.id, created_at: node.created_at }],
  '/api/v1/logs/audit': [],
  '/api/v1/health/raft': { leader: 'alpha', state: 'Leader', peers: ['alpha'], commit_index: 1, last_log_index: 1 },
  '/api/v1/stats/nodes/node-1': { node_id: node.id, range: '1h', series: {} },
  '/api/v1/stats/storage/node-1': { node_id: node.id, range: '1h', series: {} },
  '/api/v1/logs/nodes/node-1': [{ ts: node.created_at, stream: 'stdout', msg: 'Host connected' }],
  '/api/v1/auth/token': { access_token: 'story-only', token_type: 'Bearer', expires_in: 3600 },
}
export function fixtureApi() {
  return createApiClient({ baseUrl: 'https://story.invalid/api/v1', fetch: async input => {
    const data = fixtureData[new URL(String(input)).pathname]
    return new Response(JSON.stringify(data ?? { message: 'This command is unavailable in the story' }), { status: data === undefined ? 404 : 200 })
  } })
}

// Each story gets its own mutable lab, so actions never reach an appliance.
export function interactiveFixtureApi(initial: Stack[] = [{ ...stack, state: 'Idle' }]) {
  let stacks = initial.map(value => ({ ...value }))
  const containerStates: Record<string, Container['status']> = {}
  const find = (id: string) => stacks.find(value => value.id === id)!
  return {
    ...fixtureApi(),
    listStacks: async () => stacks.map(value => ({ ...value })),
    getStack: async (id: string) => ({ ...find(id) }),
    getStackConfig: async (id: string) => find(id).compose_file,
    validateCompose: async () => {},
    createStack: async (body: { name: string; compose_file: string }) => {
      const created: Stack = { ...stack, ...body, id: `stack-${stacks.length + 1}`, state: 'Idle' }
      stacks = [...stacks, created]
      return created
    },
    updateCompose: async (id: string, compose_file: string) => Object.assign(find(id), { compose_file }),
    startStack: async (id: string) => Object.assign(find(id), { state: 'RunningPrimary' }),
    stopStack: async (id: string) => Object.assign(find(id), { state: 'Idle' }),
    deleteStack: async (id: string) => { stacks = stacks.filter(value => value.id !== id) },
    listContainers: async (id: string): Promise<Container[]> => [{ id: 'container-1', name: `${find(id).name}-web`, service: 'web', image: 'nginx:alpine', status: containerStates[id] ?? (find(id).state === 'Idle' ? 'stopped' : 'running'), node_id: node.id, stack_id: id }],
    startContainer: async (id: string): Promise<Container> => ({ id: 'container-1', name: `${find(id).name}-web`, service: 'web', image: 'nginx:alpine', status: containerStates[id] = 'running', node_id: node.id, stack_id: id }),
    stopContainer: async (id: string): Promise<Container> => ({ id: 'container-1', name: `${find(id).name}-web`, service: 'web', image: 'nginx:alpine', status: containerStates[id] = 'stopped', node_id: node.id, stack_id: id }),
    execContainer: async () => ({ stdout: 'Demo command completed\n', stderr: '', exit_code: 0 }),
    getStackLogs: async (): Promise<LogLine[]> => [{ ts: node.created_at, stream: 'stdout', msg: 'Demo web service ready' }],
    getContainerLogs: async (): Promise<LogLine[]> => [{ ts: node.created_at, stream: 'stdout', msg: 'Demo web service ready' }],
    getStackStats: async () => [],
    listSnapshots: async () => [],
    createSnapshot: async () => ({ state: 'accepted' }),
    upgradeStack: async () => ({ state: 'accepted' }),
    listBackupTargets: async () => [],
    listReplication: async () => [],
  }
}
