import type { Meta, StoryObj } from '@storybook/vue3-vite'

const meta: Meta = { title: '1. Primitive/Typography' }
export default meta
export const IBMPlexMono: StoryObj = {
  render: () => ({
    setup: () => ({ sizes: [10, 11, 12, 13, 14, 15, 20], roles: [{ name: 'Column heading · uppercase / 0.7px tracking', size: 10, line: 14 }, { name: 'Telemetry / footer / count', size: 11, line: 16 }, { name: 'Status / network / group', size: 12, line: 18 }, { name: 'Control / event / node plate', size: 13, line: 18 }, { name: 'Host / app name', size: 14, line: 20 }, { name: 'Detail section / H3', size: 15, line: 21 }, { name: 'Brand / H1 lab name / H2 Apps', size: 20, line: 28 }], weights: [400, 500, 600, 700] }),
    template: `<section class="foundation"><h1>IBM Plex Mono</h1>
      <p>IBM Plex Mono throughout. Dashboard UI uses weight 500; Inter headings in the reference are normalized to the shared family. Sizes and line heights come from the dashboard frame.</p>
      <h2>Dashboard roles</h2><div v-for="role in roles" :key="role.name" class="foundation-card" style="margin-bottom: 12px"><code class="foundation-label">{{ role.size }}px / {{ role.line }}px · weight 500</code><p :style="{ fontSize: role.size + 'px', lineHeight: role.line + 'px', fontWeight: 500 }">{{ role.name }}</p></div><h2>Sizes</h2><p v-for="size in sizes" :key="size" :style="{ fontSize: size + 'px' }">{{ size }}px · FloatLab / server-01 / 0123456789</p>
      <h2>Weights</h2><p v-for="weight in weights" :key="weight" :style="{ fontWeight: weight, fontSize: '16px' }">{{ weight }} · FloatLab / Aa Bb Cc / 0123456789</p>
    </section>`,
  }),
}
