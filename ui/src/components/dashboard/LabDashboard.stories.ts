import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LabDashboard from './LabDashboard.vue'
import { hosts, apps, events } from '../../dashboard/fixtures'

const meta: Meta<typeof LabDashboard> = {
  title: '2. Pattern/Lab Dashboard', component: LabDashboard,
  args: { hosts, apps, events },
  argTypes: { onAction: { action: 'action' } },
  parameters: { layout: 'fullscreen' },
  render: (args) => ({ components: { LabDashboard }, setup: () => ({ args }), template: '<LabDashboard v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Connected: StoryObj<typeof meta> = { args: { live: true, labName: 'My lab', clusterStatus: 'Leader · 3 peers', clusterHealth: 'ready' } }
export const ClusterUnavailable: StoryObj<typeof meta> = { args: { live: true, labName: 'My lab', clusterStatus: 'Cluster status unavailable', clusterHealth: 'unknown', hosts: [], apps: [], events: [] } }
