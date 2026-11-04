import { describe, expect, it } from 'vitest'
import { composeNetwork, appItem, auditEvent, hostItem, labEvent, latest, percentage } from './live'
import { node, stack } from './live-fixtures'
describe('Live dashboard mapping', () => {
  it('maps workload history without inventing missing hosts or workloads', () => {
    const entry = { id: 'audit-1', actor: 'admin', action: 'StartStack', resource_type: 'stack', resource_id: stack.id, ts: node.created_at }
    expect(auditEvent({ ...entry, outcome: 'failed' }, [stack], [node])).toMatchObject({ id: 'audit:audit-1', tone: 'error', host: node.name, message: 'plex · failed · admin' })
    expect(auditEvent(entry, [], [])).toMatchObject({ tone: 'info', host: '—', message: 'stack-1 · recorded · admin' })
    expect(auditEvent({ ...entry, resource_id: '' }, [], []).message).toBe('stack · recorded · admin')
  })
  it('labels remote snapshots as cached without renaming local workloads', () => {
    expect(appItem({ ...stack, replica: true, stale: true }, [node]).name).toBe(stack.name + ' (cached)')
    expect(appItem(stack, [node]).name).toBe(stack.name)
  })
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
  it('only offers host switching for a running local app with a configured standby', () => {
    expect(appItem({ ...stack, state: 'RunningPrimary', secondary_node: 'node-2' }, [node]).hostSwitch).toBe('triggerFailover')
    expect(appItem({ ...stack, state: 'RunningBackup', secondary_node: 'node-2' }, [node]).hostSwitch).toBe('restoreStack')
    expect(appItem({ ...stack, state: 'Idle', secondary_node: 'node-2' }, [node]).hostSwitch).toBeUndefined()
    expect(appItem({ ...stack, replica: true, secondary_node: 'node-2' }, [node]).hostSwitch).toBeUndefined()
    expect(appItem({ ...stack, secondary_node: null }, [node]).hostSwitch).toBeUndefined()
  })

  it('reads declared app addresses and ports without inventing a network address', () => {
    const result = composeNetwork('services:\n  web:\n    ports: ["127.0.0.1:8080:80", "443:443/tcp", 5432, {published: 3000, target: 80}]\n    networks:\n      lan:\n        ipv4_address: 10.0.0.4\n')
    expect(result).toEqual({ address: '10.0.0.4', ports: ['8080', '443', '5432', '3000'] })
    expect(composeNetwork('services: {}')).toEqual({ address: '—', ports: [] })
    expect(composeNetwork('[')).toEqual({ address: '—', ports: [] })
  })

})
