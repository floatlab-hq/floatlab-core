import type { Meta, StoryObj } from '@storybook/vue3-vite'
import { IconSearchFilled, IconSettingsFilled, IconPlusFilled, IconChevronDownFilled, IconCheckFilled, IconAlertTriangleFilled, IconXFilled, IconDatabaseFilled, IconMoonFilled } from '@tabler/icons-vue'

const meta: Meta = { title: '1. Primitive/Icons' }
export default meta
export const Tabler: StoryObj = {
  args: { size: 20 },
  argTypes: { size: { control: { type: 'range', min: 12, max: 48, step: 2 } } },
  render: (args) => ({
    setup: () => ({ args, icons: { IconSearchFilled, IconSettingsFilled, IconPlusFilled, IconChevronDownFilled, IconCheckFilled, IconAlertTriangleFilled, IconXFilled, IconDatabaseFilled, IconMoonFilled } }),
    template: `<section class="foundation"><h1>Tabler filled icons</h1>
      <p>Filled icons are the FloatLab default and inherit the current text color. Adjust size in Controls. Initial sample: 20px; component choices will follow Figma.</p>
      <div class="foundation-grid"><div v-for="(icon, name) in icons" :key="name" class="foundation-card">
        <component :is="icon" :size="args.size" aria-hidden="true" /><code class="foundation-label">{{ name }}</code>
      </div></div>
    </section>`,
  }),
}
