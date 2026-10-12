import type { Meta, StoryObj } from '@storybook/vue3-vite'
import CommandConsole from './CommandConsole.vue'
import { fixtureApi } from '../dashboard/live-fixtures'
const args = { api: fixtureApi() }
const meta: Meta<typeof CommandConsole> = { title: '2. Pattern/Command Console', component: CommandConsole, args, render: args => ({ components: { CommandConsole }, setup: () => ({ args }), template: '<CommandConsole v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
