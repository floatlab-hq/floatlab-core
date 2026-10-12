import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiSwitch from './UiSwitch.vue'
import { ref } from 'vue'
const meta: Meta<typeof UiSwitch> = {
 title: '1. Primitive/Switch', component: UiSwitch, parameters: { layout: 'fullscreen' },
 render: () => ({ components: { UiSwitch }, setup() { const value = ref(true)
return { value } }, template: '<UiSwitch v-model="value" label="Resource alerts" /><UiSwitch label="Unavailable" disabled />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
