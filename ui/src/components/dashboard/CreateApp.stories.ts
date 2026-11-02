import type { Meta, StoryObj } from '@storybook/vue3-vite'
import CreateApp from './CreateApp.vue'
import { node } from '../../dashboard/live-fixtures'
const meta: Meta<typeof CreateApp> = {
  title: '2. Pattern/Create App', component: CreateApp,
  args: { nodes: [node, { ...node, id: 'node-2', name: 'lab-beta' }] },
  argTypes: { onCreate: { action: 'create' }, onClose: { action: 'close' } },
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const NoNodes: StoryObj<typeof meta> = { args: { nodes: [] } }
