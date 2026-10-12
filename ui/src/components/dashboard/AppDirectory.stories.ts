import type { Meta, StoryObj } from '@storybook/vue3-vite'
import AppDirectory from './AppDirectory.vue'
import { apps } from '../../dashboard/fixtures'

const meta: Meta<typeof AppDirectory> = {
  title: '2. Pattern/App Directory', component: AppDirectory,
  args: { apps },
  argTypes: { onState: { action: 'state' }, onTools: { action: 'tools' }, onOpen: { action: 'open' }, onNetwork: { action: 'network' }, onColumns: { action: 'columns' } },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { AppDirectory }, setup: () => ({ args }), template: '<AppDirectory v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Empty: StoryObj<typeof meta> = { args: { apps: [] } }
export const MixedStates: StoryObj<typeof meta> = {
  args: { apps: apps.slice(0, 4).map((app, index) => ({ ...app, state: (['Running', 'Stopped', 'Failed', 'Starting'] as const)[index]!, tls: index !== 2 })) },
}

export const ReadOnly: StoryObj<typeof meta> = { args: { readonly: true } }
