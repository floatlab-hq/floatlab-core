import type { Meta, StoryObj } from '@storybook/vue3-vite'
import { userEvent, within } from 'storybook/test'
import CommandConsole from './CommandConsole.vue'
import { fixtureApi } from '../dashboard/live-fixtures'
const args = { api: fixtureApi() }
const meta: Meta<typeof CommandConsole> = { title: '2. Pattern/Command Console', component: CommandConsole, args, render: args => ({ components: { CommandConsole }, setup: () => ({ args }), template: '<CommandConsole v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
export const UpdateManagementActions: StoryObj<typeof meta> = {
  play: async ({ canvasElement }) => {
    await userEvent.click(within(canvasElement).getByRole('button', { name: 'Open command palette' }))
    const command = await within(canvasElement.ownerDocument.body).findByRole('combobox', { name: 'Command' })
    await userEvent.type(command, 'upd')
    await userEvent.tab()
    await userEvent.tab()
  },
}
