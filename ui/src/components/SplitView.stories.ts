import type { Meta, StoryObj } from '@storybook/vue3-vite'
import SplitView from './SplitView.vue'
import { ref } from 'vue'
import UiButton from './UiButton.vue'
const meta: Meta<typeof SplitView> = {
 title: '3. Layout/Split View', component: SplitView, parameters: { layout: 'fullscreen' },
 render: () => ({ components: { SplitView, UiButton }, setup() { const open = ref(false)
return { open } }, template: '<UiButton variant="surface" @click="open = !open">Toggle panel</UiButton><SplitView :open="open"><p>Primary content</p><template #detail><p>Detail content</p></template></SplitView>' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
