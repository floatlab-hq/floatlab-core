import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiHealthMark from './UiHealthMark.vue'

const meta: Meta<typeof UiHealthMark> = {
  title: '1. Primitive/Health Mark', component: UiHealthMark,
  args: { state: 'ready' },
  argTypes: { state: { control: 'select', options: ['ready', 'stopped', 'standby', 'degraded', 'unknown'] } },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { UiHealthMark }, setup: () => ({ args }), template: '<UiHealthMark v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const AllStates: StoryObj<typeof meta> = {
  render: () => ({
    components: { UiHealthMark },
    template: '<div class="foundation-row"><UiHealthMark state="ready" /> Ready <UiHealthMark state="stopped" /> Stopped <UiHealthMark state="standby" /> Standby <UiHealthMark state="degraded" /> Degraded <UiHealthMark state="unknown" /> Unknown</div>',
  }),
}
