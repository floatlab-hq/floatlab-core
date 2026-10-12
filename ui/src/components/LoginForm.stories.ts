import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LoginForm from './LoginForm.vue'
const args = { error: '' }
const meta: Meta<typeof LoginForm> = { title: '2. Pattern/Login', component: LoginForm, args, render: args => ({ components: { LoginForm }, setup: () => ({ args }), template: '<LoginForm v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
