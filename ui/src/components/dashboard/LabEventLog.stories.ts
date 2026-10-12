import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LabEventLog from './LabEventLog.vue'
import { events } from '../../dashboard/fixtures'

const meta: Meta<typeof LabEventLog> = {
  title: '2. Pattern/Lab Event Log', component: LabEventLog,
  args: { events },
  argTypes: { onSelect: { action: 'select' } },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { LabEventLog }, setup: () => ({ args }), template: '<LabEventLog v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Empty: StoryObj<typeof meta> = { args: { events: [] } }

export const ReadOnly: StoryObj<typeof meta> = { args: { readonly: true } }
