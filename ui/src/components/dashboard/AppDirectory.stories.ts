import type { Meta, StoryObj } from '@storybook/vue3-vite'
import AppDirectory from './AppDirectory.vue'
import type { DirectoryApp } from '../../dashboard/types'
import { apps } from '../../dashboard/fixtures'

const meta: Meta<typeof AppDirectory> = {
  title: '2. Pattern/App Directory', component: AppDirectory,
  args: { apps },
  argTypes: { onCommand: { action: 'command' }, onOpen: { action: 'open' },  },
  parameters: { layout: 'padded', docs: { description: { component: 'Solid surface rows with small gaps and no divider lines. Only the sorted column shows a direction arrow, matching its header text color. Click a row to expand its containers and open the side panel. Up/Down selects apps in sorted order and opens their side panel; Home/End jump to the ends, Left collapses and Right expands. Enter, Space, or the app name opens details. Individual plate controls retain their own actions.' } } },
  render: (args) => ({ components: { AppDirectory }, setup: () => ({ args }), template: '<AppDirectory v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const NetworkDetails: StoryObj<typeof meta> = { args: { apps: [{ ...apps[0]!, address: '10.0.5.21', ports: ['80', '443/tcp', '8080'] }, { ...apps[1]!, address: '2001:db8::2', ports: ['2283'] }, { ...apps[2]!, address: '', ports: [] }] } }
export const Empty: StoryObj<typeof meta> = { args: { apps: [] } }
export const MixedStates: StoryObj<typeof meta> = {
  args: { apps: apps.slice(0, 4).map((app, index) => ({ ...app, state: (['Running', 'Stopped', 'Failed', 'Starting'] as const)[index]!, tls: index !== 2 })) },
}

export const ReadOnly: StoryObj<typeof meta> = { args: { readonly: true } }

const stateExamples: DirectoryApp[] = ([
  ["caddy","Running","atlas","Synced","boreal","Scheduled","cedar",null,null,1],
  ["postgres","Running","atlas","Syncing","boreal","Syncing","cedar",42,68,1],
  ["redis","Stopped","boreal","Not synced","cedar","None","",null,null,1],
  ["jellyfin","Starting","atlas","Queued","boreal","Scheduled","cedar",null,null,1],
  ["immich","Provisioning","atlas","None","","None","",null,null,1],
  ["nextcloud","Updating","boreal","Syncing","cedar","Queued","cedar",null,null,1],
  ["paperless","Stopping","delta","Offline","echo","Failed","echo",null,null,1],
  ["grafana","Failed","atlas","Failed","boreal","Synced","cedar",null,null,1],
  ["vaultwarden","RunningBackup","cedar","In use","atlas","Scheduled","boreal",null,null,1],
  ["home-assistant","FailingOver","atlas","Offline","echo","Unavailable","",null,null,1],
  ["minio","Restoring","boreal","Queued","atlas","Syncing","cedar",null,28,3],
  ["gitea","RollingBack","boreal","Synced","atlas","Failed","delta",null,null,1],
  ["uptime-kuma","Running","foxtrot","Unavailable","boreal","Unavailable","",null,null,1],
  ["whoami","Running","boreal","None","","None","",null,null,1],
  ["registry","Running","atlas","Not synced","cedar","Scheduled","boreal",null,null,1],
  ["redis-standby","RunningBackup","cedar","In use","boreal","Synced","boreal",null,null,2],
  ["caddy-restart","Running","atlas","Synced","boreal","Scheduled","cedar",null,null,1],
  ["archive","Stopped","echo","Synced","cedar","Scheduled","cedar",null,null,1],
  ["legacy-agent","Running","foxtrot","Not synced","boreal","Unknown","cedar",null,null,1],
] satisfies [string, DirectoryApp['state'], string, string, string, string, string, number | null, number | null, number][]).map(([name, state, host, replication, standby, backupLabel, backup, replicationProgress, backupProgress, count]) => ({
  ...apps[0]!, id: name, name, state, host, hostId: host, standby, backup,
  hostHealth: host === 'echo' ? 'offline' : host === 'foxtrot' ? 'unknown' : 'online',
  runtimeMotion: name === 'caddy-restart' ? 'restart' : undefined,
  hostSwitch: state === 'RunningBackup' ? 'restoreStack' : undefined,
  protectionAvailable: ![replication, backupLabel].includes('Unavailable'),
  replication: { label: replication, host: standby, hostHealth: standby === 'echo' ? 'offline' : 'online', busy: ['Syncing', 'Queued'].includes(replication), progress: replicationProgress ?? undefined, tone: replication === 'Failed' ? 'error' : ['Offline', 'Unavailable', 'In use'].includes(replication) ? 'warning' : 'default' },
  backups: { label: backupLabel, host: backup, hostHealth: backup === 'echo' ? 'offline' : 'online', busy: ['Syncing', 'Queued'].includes(backupLabel), progress: backupProgress ?? undefined, count, completedAt: backupLabel === 'Synced' ? new Date(Date.now() - 2 * 3600_000).toISOString() : undefined, tone: backupLabel === 'Failed' ? 'error' : ['Unavailable', 'Unknown'].includes(backupLabel) ? 'warning' : 'default' },
}))
export const AllStates: StoryObj<typeof meta> = { args: { apps: stateExamples }, parameters: { docs: { description: { story: 'Six-column directory with IP addresses and ports: all lifecycle states, six hosts, protection outcomes, known and unknown progress, and multi-target backups. All data is illustrative. Headers sort both directions; plate menus keep their existing actions.' } } } }

export const HostSwitch: StoryObj<typeof meta> = { args: { apps: [{ ...apps[0]!, hostSwitch: 'triggerFailover' }] } }

export const Syncing: StoryObj<typeof meta> = { args: { apps: [{ ...apps[0]!, ports: ['5432'], replication: { label: 'Syncing', host: 'node-02', hostHealth: 'online', busy: true, detail: 'Running transfer · total size unavailable' }, backups: { label: 'Syncing', host: 'node-03', hostHealth: 'online', busy: true, progress: 42 } }] } }

export const KeyboardNavigation: StoryObj<typeof meta> = {
  args: { loadContainers: async id => [{ id: `${id}-web`, node_id: 'node', stack_id: id, name: `${id}-web-1`, service: 'web', image: 'nginx:alpine', status: 'running' }, { id: `${id}-worker`, node_id: 'node', stack_id: id, name: `${id}-worker-1`, service: 'worker', image: 'alpine:3.20', status: 'stopped' }] },
}
