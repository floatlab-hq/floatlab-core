import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiTelemetry from './UiTelemetry.vue'

const meta: Meta<typeof UiTelemetry> = {
  title: '1. Primitive/Telemetry', component: UiTelemetry,
  args: { cpu: '⠴⠼⠦', memory: 38, disk: 40 },
  argTypes: {  },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { UiTelemetry }, setup: () => ({ args }), template: '<UiTelemetry v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}

export const Unavailable: StoryObj<typeof meta> = { args: { cpu: '—', memory: undefined, disk: undefined } }
