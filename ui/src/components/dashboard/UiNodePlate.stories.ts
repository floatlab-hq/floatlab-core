import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiNodePlate from './UiNodePlate.vue'

const meta: Meta<typeof UiNodePlate> = {
  title: '1. Primitive/Node Plate', component: UiNodePlate,
  args: { name: 'node-01' },
  argTypes: {  },
  parameters: { layout: 'padded', docs: { description: { component: 'Text-only host code using the same canvas background and secondary text color as the inset host label in action plates. The full host name remains available through the accessible label and tooltip.' } } },
  render: (args) => ({ components: { UiNodePlate }, setup: () => ({ args }), template: '<UiNodePlate v-bind="args" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const NamingExamples: StoryObj<typeof meta> = {
  render: () => ({
    components: { UiNodePlate },
    template: '<div class="foundation-row"><UiNodePlate name="node-01" /><UiNodePlate name="node-02" /><UiNodePlate name="node-03" /><UiNodePlate name="Everforest" /><UiNodePlate name="Death Star" /></div>',
  }),
}

export const Editable: StoryObj<typeof meta> = { args: { editable: true } }
