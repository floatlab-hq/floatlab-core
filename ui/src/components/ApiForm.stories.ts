import type { Meta, StoryObj } from '@storybook/vue3-vite'
import ApiForm from './ApiForm.vue'
import { ref } from 'vue'
import { coreForms } from '../forms'
const meta: Meta<typeof ApiForm> = {
 title: '2. Pattern/API Form', component: ApiForm, argTypes: { onSubmit: { action: 'submit' } }, parameters: { layout: 'fullscreen' },
 render: (args) => ({ components: { ApiForm }, setup() { return { args, values: ref({}), schema: coreForms.NodeCreate } }, template: '<ApiForm v-bind="args" v-model="values" :schema="schema" /><pre>{{ values }}</pre>' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
export const AlertRule: StoryObj<typeof meta> = { render: (args) => ({ components: { ApiForm }, setup: () => ({ args, values: ref({}), schema: coreForms.AlertRuleCreate }), template: '<ApiForm v-bind="args" v-model="values" :schema="schema" />' }) }
export const AddApp: StoryObj<typeof meta> = { render: (args) => ({ components: { ApiForm }, setup: () => ({ args, values: ref({}), schema: coreForms.StackCreate }), template: '<ApiForm v-bind="args" v-model="values" :schema="schema" />' }) }
