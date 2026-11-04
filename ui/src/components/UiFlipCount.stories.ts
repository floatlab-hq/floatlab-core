import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiFlipCount from './UiFlipCount.vue'
const meta: Meta<typeof UiFlipCount> = {
  title: '1. Primitive/Flip Count', component: UiFlipCount, args: { count: 0 },
  parameters: { layout: 'centered' },
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Capped: StoryObj<typeof meta> = { args: { count: 120 } }
