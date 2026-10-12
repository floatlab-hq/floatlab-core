import type { Meta, StoryObj } from '@storybook/vue3-vite'
import { ref } from 'vue'
import UiInput from './UiInput.vue'

type InputArgs = InstanceType<typeof UiInput>['$props'] & { placeholder: string; disabled: boolean }

const meta: Meta<InputArgs> = {
  title: '1. Primitive/UI Input', component: UiInput,
  args: { modelValue: '', placeholder: 'Search commands', disabled: false },
  argTypes: { disabled: { control: 'boolean' }, placeholder: { control: 'text' } },
  render: (args) => ({
    components: { UiInput },
    setup: () => ({ args, value: ref(args.modelValue) }),
    template: '<div><UiInput v-bind="args" v-model="value" aria-label="Search commands" /><p class="foundation-muted">Value: {{ value }}</p></div>',
  }),
}
export default meta
export const Playground: StoryObj<InputArgs> = {}
export const Disabled: StoryObj<InputArgs> = { args: { disabled: true, modelValue: 'Unavailable' } }

export const Field: StoryObj<InputArgs> = { args: { variant: 'field', modelValue: 'node-01', placeholder: 'Display name' } }
