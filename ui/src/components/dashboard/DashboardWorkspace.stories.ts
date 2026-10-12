import type { Meta, StoryObj } from '@storybook/vue3-vite'
import DashboardWorkspace from './DashboardWorkspace.vue'
import { hosts, apps, events } from '../../dashboard/fixtures'
const meta: Meta<typeof DashboardWorkspace> = {
 title: '2. Pattern/Dashboard Workspace', component: DashboardWorkspace, argTypes: { onAction: { action: 'action' }, onSubmit: { action: 'submit' } }, parameters: { layout: 'fullscreen' },
 render: (args) => ({ components: { DashboardWorkspace }, setup() { return { args, hosts, apps, events } }, template: '<DashboardWorkspace v-bind="args" :hosts="hosts" :apps="apps" :events="events" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const HostDetailOpen: StoryObj<typeof meta> = { render: (args) => ({ components: { DashboardWorkspace }, setup: () => ({ args, hosts, apps, events }), template: '<DashboardWorkspace v-bind="args" :hosts="hosts" :apps="apps" :events="events" initial-panel="host" />' }) }
export const AddHost: StoryObj<typeof meta> = { render: (args) => ({ components: { DashboardWorkspace }, setup: () => ({ args, hosts, apps, events }), template: '<DashboardWorkspace v-bind="args" :hosts="hosts" :apps="apps" :events="events" initial-panel="add-host" />' }) }
export const AddApp: StoryObj<typeof meta> = { render: (args) => ({ components: { DashboardWorkspace }, setup: () => ({ args, hosts, apps, events }), template: '<DashboardWorkspace v-bind="args" :hosts="hosts" :apps="apps" :events="events" initial-panel="add-app" />' }) }
export const Settings: StoryObj<typeof meta> = { render: (args) => ({ components: { DashboardWorkspace }, setup: () => ({ args, hosts, apps, events }), template: '<DashboardWorkspace v-bind="args" :hosts="hosts" :apps="apps" :events="events" initial-panel="settings" />' }) }
