import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiTabs from './UiTabs.vue'
import { ref } from 'vue'
const meta: Meta<typeof UiTabs> = {
 title: '1. Primitive/Tabs', component: UiTabs, parameters: { layout: 'fullscreen' },
 render: () => ({ components: { UiTabs }, setup() { const value = ref('overview')
const tabs = [{ id: 'overview', label: 'Overview' }, { id: 'config', label: 'Config' }, { id: 'disabled', label: 'Unavailable', disabled: true }]
return { value, tabs } }, template: '<UiTabs v-model="value" :tabs="tabs" label="Detail"><template #default="{ active }">{{ active }}</template></UiTabs>' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
