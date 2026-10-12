import { describe, expect, it } from 'vitest'
import { appItem, hostItem, labEvent, latest, percentage } from './live'
import { node, stack } from './live-fixtures'
describe('Live dashboard mapping', () => {
  it('does not manufacture missing metrics', () => {
    expect(latest()).toBeUndefined()
    expect(latest({ name: 'cpu', labels: {}, points: [{ ts: 1, value: NaN }] })).toBeUndefined()
    expect(percentage(1, 0)).toBeUndefined()
    expect(percentage(150, 100)).toBe(100)
    expect(percentage(-1, 100)).toBe(0)
    expect(hostItem(node, [stack])).toMatchObject({ cpu: '—', memory: undefined, disk: undefined, apps: 1, address: '2001:db8::1/64' })
  })
  it.each(['online', 'offline', 'degraded', 'unknown'] as const)('preserves %s health', status => {
    expect(hostItem({ ...node, status }, []).health).toBe(status === 'online' ? 'ready' : status === 'offline' ? 'stopped' : status)
  })
  it.each(['Provisioning', 'Starting', 'FailingOver', 'Restoring', 'Stopping', 'Updating', 'RollingBack', 'Failed'] as const)('retains %s lifecycle state', state => {
    expect(appItem({ ...stack, state }, [node]).state).toBe(state)
  })
  it('uses the backup host during failover and resolves names by ID', () => {
    const backup = { ...node, id: 'node-2', name: 'lab-beta' }
    const active = { ...stack, state: 'RunningBackup' as const, secondary_node: backup.id }
    expect(appItem(active, [node, backup])).toMatchObject({ state: 'Running', host: 'lab-beta', standby: 'lab-alpha', tls: undefined })
    expect(appItem({ ...stack, state: 'Idle' }, []).state).toBe('Stopped')
    expect(hostItem(backup, [active]).apps).toBe(1)
  })
  it('maps critical notifications to errors', () => {
    expect(labEvent({ id: 'a', severity: 'critical', state: 'unread', title: 'Failure', body: 'Failed', created_at: node.created_at, node_id: node.id }, [node])).toMatchObject({ tone: 'error', host: node.name, source: 'Failure' })
  })
})
