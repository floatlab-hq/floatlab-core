import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LiveDashboard from './LiveDashboard.vue'
import { fixtureApi } from '../../dashboard/live-fixtures'
const args = { api: fixtureApi() }
const meta: Meta<typeof LiveDashboard> = { title: '2. Pattern/Live Dashboard', component: LiveDashboard, args, render: args => ({ components: { LiveDashboard }, setup: () => ({ args }), template: '<LiveDashboard v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
