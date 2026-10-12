import type { Meta, StoryObj } from '@storybook/vue3-vite'
import { ref } from 'vue'
import UiTextarea from './UiTextarea.vue'
const meta: Meta<typeof UiTextarea> = { title: '1. Primitive/Textarea', component: UiTextarea, render: () => ({ components: { UiTextarea }, setup: () => ({ value: ref('services:\n  postgres:\n    image: postgres:17') }), template: '<UiTextarea v-model="value" aria-label="Compose file" rows="6" />' }) }
export default meta
export const Default: StoryObj<typeof meta> = {}
export const Disabled: StoryObj<typeof meta> = { render: () => ({ components: { UiTextarea }, template: '<UiTextarea disabled aria-label="Unavailable" model-value="Read only preview" />' }) }
