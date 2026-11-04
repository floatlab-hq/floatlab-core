import type { BackupTarget, Node, Stack } from '@floatlab/client'
import type { TransferStatus } from './types'

export function transferStatus(jobs: Record<string, unknown>[], empty = 'Not synced'): TransferStatus {
  const ordered = [...jobs].sort((a, b) => String(b.created_at ?? '').localeCompare(String(a.created_at ?? '')))
  const job = ordered.find(item => ['running', 'idle', 'pending'].includes(String(item.state))) ?? ordered[0]
  if (!job) return { label: empty }
  const busy = ['running', 'idle', 'pending'].includes(String(job.state))
  const bytes = typeof job.bytes_transferred === 'number' ? job.bytes_transferred : typeof job.bytes_sent === 'number' ? job.bytes_sent : undefined
  const detail = job.error ? String(job.error) : [job.state, bytes === undefined ? '' : `${(bytes / 1024 / 1024).toFixed(1)} MiB transferred`, job.completed_at || job.started_at || job.created_at].filter(Boolean).join(' · ')
  return { label: job.state === 'running' ? 'Syncing' : busy ? 'Queued' : job.state === 'complete' ? 'Synced' : job.state === 'failed' ? 'Failed' : String(job.state), busy, completedAt: job.state === 'complete' && typeof job.completed_at === 'string' ? job.completed_at : undefined, tone: job.state === 'failed' ? 'error' : 'default', detail }
}

export function protectionStatus(stack: Stack, nodes: Node[], targets: BackupTarget[], jobs: Record<string, unknown>[], error?: string) {
  const standby = stack.state === 'RunningBackup' ? stack.primary_node : stack.secondary_node
  const node = nodes.find(item => item.id === standby)
  const backups = targets.filter(target => target.enabled)
  const standbyJobs = jobs.filter(job => job.dest_node_id === standby)
  const backupJobs = jobs.filter(job => backups.some(target => target.node_id === job.dest_node_id && target.dataset === job.dataset))
  const replication: TransferStatus = !standby ? { label: 'None' } : stack.state === 'RunningBackup' ? { label: 'In use', tone: 'warning', detail: 'App is serving from standby; replication is suspended' } : error ? { label: 'Unavailable', tone: 'warning', detail: error } : node?.status === 'offline' ? { label: 'Offline', tone: 'warning' } : transferStatus(standbyJobs)
  const backup: TransferStatus = error ? { label: 'Unavailable', tone: 'warning', detail: error } : backups.length ? transferStatus(backupJobs, 'Scheduled') : { label: 'None' }
  return {
    replication: { ...replication, host: node?.name ?? standby ?? '', hostHealth: node?.status },
    backups: { ...backup, host: nodes.find(item => item.id === backups[0]?.node_id)?.name ?? backups[0]?.node_id ?? '', hostHealth: nodes.find(item => item.id === backups[0]?.node_id)?.status, count: backups.length },
  }
}
