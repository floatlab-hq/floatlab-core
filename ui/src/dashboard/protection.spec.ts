import { describe, expect, it } from 'vitest'
import { transferStatus, protectionStatus } from './protection'
import { node, stack } from './live-fixtures'
import type { BackupTarget } from '@floatlab/client'
const targets: BackupTarget[] = [{ stack_id: stack.id, dataset: 'floatlab/plex/data', node_id: 'backup', source_node_id: node.id, enabled: true, cleanup_pending: false, schedule: 'h/24', local_policy: 'h/24' }]
describe('Protection status', () => {
  it('prioritizes active work and preserves real phase and transferred bytes without inventing a total', () => {
    const status = transferStatus([{ state: 'complete', created_at: '2026-11-04' }, { state: 'running', created_at: '2026-11-03', bytes_transferred: 1048576 }])
    expect(status).toMatchObject({ label: 'Syncing', busy: true })
    expect(status.detail).toContain('1.0 MiB transferred')
    expect(status.progress).toBeUndefined()
    expect(transferStatus([{ state: 'idle' }])).toMatchObject({ label: 'Queued', busy: true })
    expect(transferStatus([{ state: 'failed', error: 'Disk full' }])).toMatchObject({ label: 'Failed', tone: 'error', detail: 'Disk full' })
    expect(transferStatus([{ state: 'complete', completed_at: '2026-11-04T10:00:00Z' }])).toMatchObject({ label: 'Synced', completedAt: '2026-11-04T10:00:00Z' })
    expect(transferStatus([])).toEqual({ label: 'Not synced' })
  })
  it('separates standby replication from per-dataset backups', () => {
    const nodes = [node, { ...node, id: 'standby', name: 'Beta' }, { ...node, id: 'backup', name: 'Gamma' }]
    const jobs = [{ dest_node_id: 'standby', state: 'running' }, { dest_node_id: 'backup', dataset: targets[0]!.dataset, state: 'failed', error: 'Disk full' }]
    const status = protectionStatus({ ...stack, secondary_node: 'standby' }, nodes, targets, jobs)
    expect(status.replication).toMatchObject({ label: 'Syncing', busy: true, host: 'Beta' })
    expect(status.backups).toMatchObject({ label: 'Failed', host: 'Gamma', count: 1 })
    expect(protectionStatus(stack, nodes, [], []).backups.label).toBe('None')
    expect(protectionStatus(stack, nodes, targets, []).backups.label).toBe('Scheduled')
  })
  it('does not label missing or unreachable data as healthy', () => {
    expect(protectionStatus(stack, [node], [], []).replication.label).toBe('None')
    expect(protectionStatus({ ...stack, secondary_node: 'offline' }, [{ ...node, id: 'offline', status: 'offline' }], [], []).replication.label).toBe('Offline')
    expect(protectionStatus(stack, [node], [], [], 'Unavailable').backups).toMatchObject({ label: 'Unavailable', detail: 'Unavailable' })
  })
  it('uses the primary as the alternate while the app runs on standby', () => {
    expect(protectionStatus({ ...stack, state: 'RunningBackup', secondary_node: 'standby' }, [node], [], []).replication.host).toBe(node.name)
  })
})
