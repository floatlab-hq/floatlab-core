import type { HostItem, DirectoryApp, LabEvent } from './types'

export const hosts: HostItem[] = [
  { id: 'node-01', name: 'node-01', health: 'ready', network: '172.16.23.13', address: '172.16.23.13', apps: 3, standby: 7, backupAttention: true, cpu: '⠴⠼⠦', memory: 38, disk: 40 },
  { id: 'node-02', name: 'node-02', health: 'stopped', network: '172.16.23.13', address: '172.16.23.13', apps: 3, cpu: '⠴⠼⠦', memory: 38, disk: 40 },
  { id: 'node-03', name: 'node-03', health: 'standby', apps: 2, cpu: '⠴⠼⠦', memory: 38, disk: 40 },
]

export const events: LabEvent[] = [
  { id: 'backup', tone: 'error', time: '17:10', host: 'node-01', source: 'APP-STACK', message: 'Postgres backup failed. Last successful backup yesterday, 08:00' },
  { id: 'sync', tone: 'info', time: '17:02', host: 'node-01', source: 'HA', message: 'HA STANDBY for Grafana initial sync complete. 1m 42s.' },
  { id: 'api', tone: 'warning', time: '16:45', host: 'node-02', source: 'API-SERVER', message: 'API response delay detected. Average time exceeded 500ms.' },
]

export const apps: DirectoryApp[] = [
  ['postgres', 'Postgres', 'imgSelfhStPostgresqlMonochrome', 'TEST'],
  ['immich', 'immich', 'imgSelfhStImmichMonochrome', 'TEST'],
  ['proxy', 'proxy', 'imgSelfhStNginxMonochrome', 'PROD'],
  ['company', 'company-website', 'imgSelfhStNginxMonochrome', 'TEST'],
  ['gateway', 'gateway', 'imgSelfhStNginxMonochrome', 'TEST'],
  ['paperless', 'paperless', 'imgSelfhStPaperlessNgxMonochrome', 'TEST'],
].map(([id, name, logo, group]) => ({
  id: id!, name: name!, logo: `/assets/dashboard/${logo}.svg`, group: group!,
  state: 'Running', address: '172.16.23.13', host: 'node-01', standby: 'node-02', backup: 'node-03', tls: true,
}))
