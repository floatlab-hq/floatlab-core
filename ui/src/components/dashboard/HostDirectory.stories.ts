import type { Meta, StoryObj } from '@storybook/vue3-vite'
import HostDirectory from './HostDirectory.vue'
import { hosts } from '../../dashboard/fixtures'

const meta: Meta<typeof HostDirectory> = {
  title: '2. Pattern/Host Directory', component: HostDirectory,
  args: { hosts },
  argTypes: { onTools: { action: 'tools' }, onInspect: { action: 'inspect' }, onConnect: { action: 'connect' } },
  parameters: { layout: 'padded', docs: { description: { component: 'Host names align with the Name header in both wide and compact layouts. Click anywhere on a row, or focus it and press Enter or Space, to open details. Individual controls retain their own actions.' } } },
  render: (args) => ({ components: { HostDirectory }, setup: () => ({ args }), template: '<HostDirectory v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
