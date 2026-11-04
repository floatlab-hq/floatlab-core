import type { Meta, StoryObj } from '@storybook/vue3-vite'
import StatusActionMenu from './StatusActionMenu.vue'
const meta: Meta<typeof StatusActionMenu> = { title: '2. Pattern/Status Action Menu', component: StatusActionMenu, args: { status: 'Running' }, argTypes: { onOperation: { action: 'operation' } } }
export default meta
export const Running: StoryObj<typeof meta> = {}
export const Starting: StoryObj<typeof meta> = { args: { status: 'Starting', pendingOperation: 'start' } }
export const Restarting: StoryObj<typeof meta> = { args: { status: 'Restarting', pendingOperation: 'restart' } }
