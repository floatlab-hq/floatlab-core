import type { Meta, StoryObj } from '@storybook/vue3-vite'
import AppPlateActions from './AppPlateActions.vue'
import { apps } from '../../dashboard/fixtures'
const meta: Meta<typeof AppPlateActions> = { title: '2. Pattern/App Plate Actions', component: AppPlateActions, args: { app: { ...apps[0]!, hostId: 'node-01', hostHealth: 'online', protectionAvailable: true, replication: { label: 'Syncing', host: 'node-02', hostHealth: 'online', busy: true }, backups: { label: 'Scheduled', host: 'node-03', hostHealth: 'online' } } }, argTypes: { onAction: { action: 'action' } }, render: args => ({ components: { AppPlateActions }, setup: () => ({ args }), template: '<div style="display:flex;gap:12px"><AppPlateActions v-bind="args" /></div>' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Cached: StoryObj<typeof meta> = { args: { app: { ...meta.args!.app!, replica: true } } }
export const ReadOnly: StoryObj<typeof meta> = { args: { readonly: true } }
export const Movable: StoryObj<typeof meta> = { args: { kind: 'runtime', app: { ...meta.args!.app!, moveAvailable: true } } }

export const Restarting: StoryObj<typeof meta> = { args: { app: { ...meta.args!.app!, runtimeMotion: 'restart' } } }

export const Pending: StoryObj<typeof meta> = { args: { app: { ...meta.args!.app!, actionPending: true } } }
