import type { Meta, StoryObj } from '@storybook/vue3-vite'
import { activeTheme, themes } from '../themes'

const meta: Meta = { title: '1. Primitive/Colors' }
export default meta

export const Palette: StoryObj = {
  render: () => ({
    setup: () => ({
      theme: activeTheme,
      roles: {
        Background: '--bg', Panel: '--panel', Void: '--void', Raised: '--raised',
        Text: '--text', Secondary: '--dim', Border: '--line', Accent: '--green',
        Success: '--success', Warning: '--warning', Error: '--error',
        'Detail panel / switch off': '--surface-recessed', 'Dashboard canvas': '--surface-canvas', 'Dashboard surface': '--surface-default',
        'Primary text': '--text-primary', 'Secondary text': '--text-secondary', 'Inverse text / plate shape': '--text-inverse',
        'Node plate block': '--omarchy-foreground',
      },
      names: Object.values(themes).map((theme) => theme.name).join(' · '),
    }),
    template: `<section class="foundation">
      <h1>{{ theme.name }}</h1>
      <p>Use the Theme toolbar to switch every component and view. Default: Everforest.</p>
      <h2>FloatLab UI colors</h2>
      <div class="foundation-grid">
        <div v-for="(token, role) in roles" :key="role" class="foundation-card">
          <div class="foundation-swatch" :style="{ background: 'var(' + token + ')' }"></div>
          <strong class="foundation-label">{{ role }}</strong><code class="foundation-label">{{ token }}</code>
        </div>
      </div>
      <h2>Original Omarchy palette · {{ theme.colors.mode }}</h2>
      <div class="foundation-grid">
        <template v-for="(value, key) in theme.colors" :key="key">
          <div v-if="key !== 'mode'" class="foundation-card">
            <div class="foundation-swatch" :style="{ background: value }"></div>
            <strong class="foundation-label">{{ key }}</strong><code class="foundation-label">{{ value }}</code>
          </div>
        </template>
      </div>
      <h2>Available themes</h2><p>{{ names }}</p>
      <p class="foundation-muted">Add a compatible colors.toml with pnpm theme:add, then restart Storybook. Original palette colors are preserved; UI roles use shared CSS variables.</p>
    </section>`,
  }),
}
