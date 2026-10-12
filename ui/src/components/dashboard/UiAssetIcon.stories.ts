import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiAssetIcon from './UiAssetIcon.vue'

const meta: Meta<typeof UiAssetIcon> = {
  title: '1. Primitive/Asset Icon', component: UiAssetIcon,
  args: { src: '/assets/dashboard/imgSelfhStPostgresqlMonochrome.svg', size: 20 },
  argTypes: {  },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { UiAssetIcon }, setup: () => ({ args }), template: '<UiAssetIcon v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
