import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiDropdown from './UiDropdown.vue'
import UiButton from './UiButton.vue'

const meta: Meta<typeof UiDropdown> = {
  title: '1. Primitive/Dropdown', component: UiDropdown,
  args: { label: 'App actions', items: [{ id: 'stop', label: 'Stop' }, { id: 'restart', label: 'Restart' }, { id: 'current', label: 'Current host', disabled: true }] },
  argTypes: { onSelect: { action: 'select' } },
  render: (args) => ({ components: { UiDropdown, UiButton }, setup: () => ({ args }), template: '<UiDropdown v-bind="args"><template #trigger="{ toggle, open, controls }"><UiButton variant="surface" aria-haspopup="menu" :aria-expanded="open" :aria-controls="controls" @click="toggle">Actions</UiButton></template></UiDropdown>' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Unavailable: StoryObj<typeof meta> = { args: { items: [{ id: 'unavailable', label: 'No host switch available', disabled: true }] } }
