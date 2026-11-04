import { ref } from 'vue'
import UiButton from '../UiButton.vue'
import type { Meta, StoryObj } from '@storybook/vue3-vite'
import DashboardNavigation from './DashboardNavigation.vue'

const meta: Meta<typeof DashboardNavigation> = {
  title: '2. Pattern/Dashboard Navigation', component: DashboardNavigation,
  args: {},
  argTypes: { onCommand: { action: 'command' }, onSettings: { action: 'settings' }, onLogout: { action: 'logout' }, onNodeKey: { action: 'node key' } },
  parameters: { layout: 'padded' },
  render: (args) => ({ components: { DashboardNavigation }, setup: () => ({ args }), template: '<DashboardNavigation v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}

export const ErrorsStacking: StoryObj<typeof meta> = {
  render: () => ({
    components: { DashboardNavigation, UiButton },
    setup() { const count = ref(2); const open = ref(false); return { count, open } },
    template: `<div style="display:grid;gap:32px"><DashboardNavigation :error-count="count" :warning-count="1" :events-open="open" @events="open = !open" /><div style="display:flex;gap:12px"><UiButton @click="count++">Add error</UiButton><UiButton @click="count += 5">Add 5 errors</UiButton><UiButton @click="count = 0">Reset count</UiButton></div><p>Use Add error to try the flip. Events toggles the button’s expanded state.</p></div>`,
  }),
}
export const Warnings: StoryObj<typeof meta> = { args: { warningCount: 4 } }
