import type { Meta, StoryObj } from '@storybook/vue3-vite'
import DashboardNavigation from './DashboardNavigation.vue'

const meta: Meta<typeof DashboardNavigation> = {
  title: '2. Pattern/Dashboard Navigation', component: DashboardNavigation,
  args: {},
  argTypes: { onCommand: { action: 'command' }, onSettings: { action: 'settings' } },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { DashboardNavigation }, setup: () => ({ args }), template: '<DashboardNavigation v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
