import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiButton from './UiButton.vue'

type ButtonArgs = InstanceType<typeof UiButton>['$props'] & { disabled: boolean }

const meta: Meta<ButtonArgs> = {
  title: '1. Primitive/UI Button', component: UiButton,
  args: { variant: 'primary', size: 'default', disabled: false },
  argTypes: {
    variant: { control: 'select', options: ['default', 'plain', 'primary', 'outline', 'ghost', 'surface'] },
    size: { control: 'select', options: ['default', 'small'] },
    disabled: { control: 'boolean' },
  },
  render: (args) => ({ components: { UiButton }, setup: () => ({ args }), template: '<UiButton v-bind="args">Save changes</UiButton>' }),
}
export default meta
export const Playground: StoryObj<ButtonArgs> = {}
export const Variants: StoryObj<ButtonArgs> = {
  render: () => ({
    components: { UiButton },
    template: `<div class="foundation-row"><UiButton variant="surface">Surface</UiButton><UiButton>Default</UiButton><UiButton variant="plain">Plain</UiButton><UiButton variant="primary">Primary</UiButton><UiButton variant="outline">Outline</UiButton><UiButton variant="ghost">Ghost</UiButton><UiButton variant="outline" size="small">Small</UiButton><UiButton disabled>Disabled</UiButton></div>`,
  }),
}
