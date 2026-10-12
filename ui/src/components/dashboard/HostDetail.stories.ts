import type { Meta, StoryObj } from '@storybook/vue3-vite'
import HostDetail from './HostDetail.vue'
import { hosts, apps } from '../../dashboard/fixtures'
const meta: Meta<typeof HostDetail> = {
 title: '2. Pattern/Host Detail', component: HostDetail, argTypes: { onAction: { action: 'action' }, onSave: { action: 'save' }, onClose: { action: 'close' } }, parameters: { layout: 'fullscreen' },
 render: (args) => ({ components: { HostDetail }, setup() { return { args, host: hosts[0]!, apps } }, template: '<HostDetail v-bind="args" :host="host" :apps="apps" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
