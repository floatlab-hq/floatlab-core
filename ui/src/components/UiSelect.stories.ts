import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiSelect from './UiSelect.vue'
import { ref } from 'vue'
const meta: Meta<typeof UiSelect> = {
 title: '1. Primitive/Select', component: UiSelect, parameters: { layout: 'fullscreen' },
 render: () => ({ components: { UiSelect }, setup() { const value = ref('floatlab')
const many = ref(['CPU', 'Memory'])
const options = ['floatlab', 'archive'].map(value => ({ value, label: value }))
const categories = ['CPU', 'Memory', 'Storage'].map(value => ({ value, label: value }))
return { value, many, options, categories } }, template: '<UiSelect v-model="value" :options="options" aria-label="Storage pool" /><UiSelect v-model="many" :options="categories" multiple aria-label="Alert categories" />' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
