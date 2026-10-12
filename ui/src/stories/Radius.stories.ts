import type { Meta, StoryObj } from '@storybook/vue3-vite'
import UiButton from '../components/UiButton.vue'
import UiInput from '../components/UiInput.vue'

const meta: Meta = { title: '1. Primitive/Radius' }
export default meta
export const TwoPixels: StoryObj = {
  render: () => ({
    components: { UiButton, UiInput },
    template: `<section class="foundation"><h1>Border radius · 2px</h1>
      <p>Shared token: <code>--radius: 2px</code>. Square, borderless controls retain their existing shape.</p>
      <div class="foundation-row"><UiButton variant="primary">Primary button</UiButton><UiInput aria-label="Radius preview" placeholder="Input" /><div class="foundation-card">Panel · 2px</div></div>
    </section>`,
  }),
}
