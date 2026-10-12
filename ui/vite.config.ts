import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'
import { loadEnv } from 'vite'

export default defineConfig(({ mode }) => ({
  plugins: [vue()],
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.ts', 'scripts/**/*.spec.ts'],
  },
  server: { proxy: { '/api': loadEnv(mode, '.', '').VITE_API_PROXY_TARGET || 'http://localhost:8080' } },
}))
