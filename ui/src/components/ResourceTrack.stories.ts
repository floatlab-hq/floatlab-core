import type { Meta, StoryObj } from '@storybook/vue3-vite'
import ResourceTrack from './ResourceTrack.vue'

const meta: Meta<typeof ResourceTrack> = {
  title: '1. Primitive/Resource Track', component: ResourceTrack,
  args: { percent: 38, compact: true, danger: false, reverse: false },
  argTypes: { percent: { control: { type: 'range', min: 0, max: 100 } } },
}
export default meta
export const Compact: StoryObj<typeof meta> = {}
export const Standard: StoryObj<typeof meta> = { args: { compact: false } }
