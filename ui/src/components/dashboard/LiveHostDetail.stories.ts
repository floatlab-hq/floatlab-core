import type { Meta, StoryObj } from '@storybook/vue3-vite'
import LiveHostDetail from './LiveHostDetail.vue'
import { node } from '../../dashboard/live-fixtures'
const args = { node, apps: [] }
const meta: Meta<typeof LiveHostDetail> = { title: '2. Pattern/Live Host Detail', component: LiveHostDetail, args, render: args => ({ components: { LiveHostDetail }, setup: () => ({ args }), template: '<LiveHostDetail v-bind="args" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
