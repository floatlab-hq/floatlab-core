import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiStatusBadge from './UiStatusBadge.vue'

const meta: Meta<typeof UiStatusBadge> = {
  title: '1. Primitive/Status Badge', component: UiStatusBadge,
  args: { state: 'Running' },
  argTypes: { state: { control: 'select', options: ['Running', 'Stopped', 'Failed', 'Provisioning', 'Starting', 'FailingOver', 'Restoring', 'Stopping', 'Updating', 'RollingBack'] } },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { UiStatusBadge }, setup: () => ({ args }), template: '<UiStatusBadge v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const AllStates: StoryObj<typeof meta> = {
  render: () => ({
    components: { UiStatusBadge },
    template: '<div class="foundation-row"><UiStatusBadge state="Running" /><UiStatusBadge state="Stopped" /><UiStatusBadge state="Failed" /><UiStatusBadge state="Starting" /></div>',
  }),
}

export const Updating: StoryObj<typeof meta> = { args: { state: 'Updating' } }
export const ReadOnly: StoryObj<typeof meta> = { args: { disabled: true } }
