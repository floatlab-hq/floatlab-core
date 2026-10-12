import type { AppRecord, Command, Server } from './types'

export const server: Server = {
  name: 'Cavs Server',
  temperature: '45°C',
  uptime: '35 days',
  metrics: [
    { id: 'cpu', value: '77%', detail: '5.1 GHz', percent: 77, history: [74, 77, 76, 73] },
    { id: 'memory', value: '56%', detail: '128 GB', percent: 56, history: [55, 55, 55, 55] },
    { id: 'storage', value: '53%', detail: '14 TB', percent: 53, history: [53, 53, 53, 53] },
    { id: 'disk', value: '35%', detail: '24 GB', percent: 35, history: [41, 47, 41, 34] },
  ],
  network: {
    up: { label: '148 MB', percent: 48 },
    down: { label: '11 MB', percent: 19 },
  },
}

export const apps: AppRecord[] = [
  {
    id: 'immich', name: 'Immich', icon: '✣', iconClass: 'muted', status: 'Starting', pendingOperation: 'start',
    links: ['caddy:8080', 'web:7070', 'webadmin', 'ssh'], notice: '◉ Auto Updating in 3 days',
    cpu: { label: '2%', percent: 2.4 }, memory: { label: '2.6 GB', percent: 2 }, storage: { label: '0.1 TB', percent: 1 }, disk: { label: '1%', percent: 1.1 },
    network: { up: { label: '5 MB', percent: 1.6 }, down: { label: '1 MB', percent: 1.4 } },
  },
  {
    id: 'compose', name: 'My compose', icon: '◇', iconClass: 'compose', status: 'Failed', address: '172.16.24.21',
    notice: 'Exit Code 126: Permission Denied.', noticeTone: 'error',
    cpu: { label: '4%', percent: 4 }, memory: { label: '2.3 GB', percent: 1.8 }, storage: { label: '0.3 TB', percent: 2 }, disk: { label: '2%', percent: 1.6 },
    network: { up: { label: '2 MB', percent: 0.7 }, down: { label: '1 MB', percent: 1.2 } },
  },
  {
    id: 'minecraft', name: 'Minecraft', icon: '▧', iconClass: 'cube', status: 'Restarting', pendingOperation: 'restart', address: '172.16.24.24',
    notice: '◉ Auto Updating in 3 days',
    cpu: { label: '46%', percent: 46.5 }, memory: { label: '27 GB', percent: 21.1 }, storage: { label: '2.2 TB', percent: 16 }, disk: { label: '24%', percent: 24.1 },
    network: { up: { label: '82 MB', percent: 26.6 }, down: { label: '5 MB', percent: 8.5 } },
  },
  {
    id: 'photo-backup', name: 'photo-backup', icon: '◇', iconClass: 'compose healthy', status: 'Running', address: '172.16.24.25',
    notice: 'Security Issue Found', noticeTone: 'warning',
    cpu: { label: '24%', percent: 24.3 }, memory: { label: '40 GB', percent: 31.1 }, storage: { label: '4.8 TB', percent: 34 }, disk: { label: '9%', percent: 8.6 },
    network: { up: { label: '58 MB', percent: 18.8 }, down: { label: '4 MB', percent: 8 } },
  },
]

export const commands: Command[] = [
  { id: 'immich', label: 'Open Immich', shortcut: '↵' },
  { id: 'restart', label: 'Restart Minecraft', shortcut: 'R' },
  { id: 'alerts', label: 'Review alerts', shortcut: 'A' },
]
