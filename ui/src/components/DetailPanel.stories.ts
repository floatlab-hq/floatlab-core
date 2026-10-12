import type { Meta, StoryObj } from '@storybook/vue3-vite'
import DetailPanel from './DetailPanel.vue'

const meta: Meta<typeof DetailPanel> = {
 title: '2. Pattern/Detail Panel', component: DetailPanel, parameters: { layout: 'fullscreen' },
 render: () => ({ components: { DetailPanel }, setup() {
return {  } }, template: '<DetailPanel title="Host detail"><p>Reusable detail, settings and creation panel.</p></DetailPanel>' }),
}
export default meta
export const Default: StoryObj<typeof meta> = {}
