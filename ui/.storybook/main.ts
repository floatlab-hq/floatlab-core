import { env } from 'node:process'
import type { StorybookConfig } from '@storybook/vue3-vite'

const config: StorybookConfig = {
  stories: ['../src/**/*.stories.ts'],
  addons: ['@storybook/addon-docs'],
  framework: '@storybook/vue3-vite',
  staticDirs: ['../public'],
  core: { disableTelemetry: true },
  viteFinal: (config) => ({ ...config, base: env.BASE_PATH || '/' }),
}

export default config
