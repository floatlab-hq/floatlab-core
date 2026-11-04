import { ref } from 'vue'
import UiButton from '../UiButton.vue'
import DetailPanel from '../DetailPanel.vue'
import LabEventLog from './LabEventLog.vue'
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
export const Connected: StoryObj<typeof meta> = { args: { live: true, labName: 'My lab' } }
export const Empty: StoryObj<typeof meta> = { args: { live: true, labName: 'My lab', hosts: [], apps: [], events: [] } }

export const EventsStacking: StoryObj<typeof meta> = {
  render: (args) => ({
    components: { LabDashboard, UiButton, DetailPanel, LabEventLog },
    setup() {
      const recent = ref([...events])
      const open = ref(false)
      function addError() {
        recent.value = [{ id: `demo-${recent.value.length}`, tone: 'error', time: 'Just now', host: 'lab-alpha', source: 'Backup', message: 'Backup failed · connection timed out' }, ...recent.value]
      }
      return { args, recent, open, addError }
    },
    template: `<LabDashboard v-bind="args" live :events="recent" :detail-open="open" :events-open="open" @action="name => { if (name === 'events') open = !open }"><template #detail><DetailPanel title="Recent lab events" @close="open = false"><LabEventLog :events="recent" readonly /></DetailPanel></template><template #footer-actions><div style="display:flex;align-items:center;gap:12px;margin-left:auto"><span>Motion preview</span><UiButton @click="addError">Add error</UiButton><UiButton @click="recent = []">Reset events</UiButton></div></template></LabDashboard>`,
  }),
}
