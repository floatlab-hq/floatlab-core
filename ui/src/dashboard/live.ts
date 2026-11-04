import type { AuditEntry, Node, Stack, NodeStats, StorageStats, Notification, components } from '@floatlab/client'
import { parse } from 'yaml'
import type { HostItem, DirectoryApp, LabEvent } from './types'

export function latest(series?: components['schemas']['MetricSeries']): number | undefined {
  const value = series?.points.at(-1)?.value
  return value !== undefined && Number.isFinite(value) ? value : undefined
}

export function percentage(used?: number, total?: number): number | undefined {
  return used !== undefined && total !== undefined && total > 0
    ? Math.max(0, Math.min(100, used / total * 100)) : undefined
}

export function hostItem(node: Node, stacks: Stack[], stats?: NodeStats, storage?: StorageStats): HostItem {
  const cpu = latest(stats?.series.cpu_usage_percent)
  return {
    id: node.id, name: node.name,
    health: node.status === 'online' ? 'ready' : node.status === 'offline' ? 'stopped' : node.status,
    address: node.addresses.map(address => address.address).join(', '),
    network: node.addresses.map(address => address.interface).join(', '),
    apps: stacks.filter(stack => (stack.state === 'RunningBackup' ? stack.secondary_node : stack.primary_node) === node.id).length,
    standby: stacks.filter(stack => stack.secondary_node === node.id && stack.state !== 'RunningBackup').length,
    cpu: cpu === undefined ? '—' : `${cpu.toFixed(1)}%`,
    memory: percentage(latest(stats?.series.memory_used_bytes), latest(stats?.series.memory_total_bytes)),
    disk: percentage(latest(storage?.series.zfs_pool_alloc_bytes), latest(storage?.series.zfs_pool_size_bytes)),
  }
}

export function composeNetwork(source: string): { address: string; ports: string[] } {
  try {
    const compose = parse(source) as { services?: Record<string, { ports?: (string | number | { published?: string | number; target?: number })[]; networks?: Record<string, { ipv4_address?: string }> }> }
    const services = Object.values(compose?.services ?? {})
    const address = services.flatMap(service => Object.values(service?.networks ?? {}).map(network => network?.ipv4_address)).find(Boolean) ?? '—'
    const ports = [...new Set(services.flatMap(service => Array.isArray(service?.ports) ? service.ports.map(port => typeof port === 'object' && port ? String(port.published ?? port.target ?? '') : String(port).split('/')[0]!.split(':').slice(-2, -1)[0] ?? String(port).split('/')[0]!) : []))].filter(Boolean)
    return { address, ports }
  } catch { return { address: '—', ports: [] } }
}

export function appItem(stack: Stack, nodes: Node[]): DirectoryApp {
  const nodeName = (id?: string | null) => nodes.find(node => node.id === id)?.name ?? id ?? ''
  return {
    id: stack.id, name: stack.name + (stack.replica ? ' (cached)' : ''), logo: '',
    state: stack.state === 'RunningPrimary' || stack.state === 'RunningBackup' ? 'Running'
      : stack.state === 'Idle' ? 'Stopped' : stack.state,
    hostId: (stack.state === 'RunningBackup' ? stack.secondary_node : stack.primary_node) ?? undefined,
    hostHealth: nodes.find(node => node.id === (stack.state === 'RunningBackup' ? stack.secondary_node : stack.primary_node))?.status,
    host: nodeName(stack.state === 'RunningBackup' ? stack.secondary_node : stack.primary_node),
    standby: nodeName(stack.state === 'RunningBackup' ? stack.primary_node : stack.secondary_node),
    ...composeNetwork(stack.compose_file), backup: '', group: '—', tls: undefined, replica: stack.replica,
    hostSwitch: stack.secondary_node && !stack.replica ? stack.state === 'RunningPrimary' ? 'triggerFailover' : stack.state === 'RunningBackup' ? 'restoreStack' : undefined : undefined,
  }
}

export function labEvent(notification: Notification, nodes: Node[]): LabEvent {
  return {
    id: notification.id, tone: notification.severity === 'critical' ? 'error' : notification.severity,
    time: new Date(notification.created_at).toLocaleString(),
    host: nodes.find(node => node.id === notification.node_id)?.name ?? notification.node_id ?? '—',
    source: notification.kind ?? notification.title, message: notification.body,
  }
}

export function auditEvent(entry: AuditEntry, stacks: Stack[], nodes: Node[]): LabEvent {
  const stack = stacks.find(stack => stack.id === entry.resource_id)
  return {
    id: `audit:${entry.id}`, tone: /fail|error/i.test(entry.outcome ?? '') ? 'error' : 'info',
    time: new Date(entry.ts).toLocaleString(),
    host: nodes.find(node => node.id === stack?.primary_node)?.name ?? '—',
    source: entry.action,
    message: `${stack?.name ?? (entry.resource_id || entry.resource_type)} · ${entry.outcome ?? 'recorded'} · ${entry.actor}`,
  }
}
