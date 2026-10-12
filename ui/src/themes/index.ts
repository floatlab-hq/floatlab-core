import { shallowRef } from 'vue'
import { parsePalette } from './parse-palette'

const files = import.meta.glob('./palettes/*.toml', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

export const themes = Object.fromEntries(Object.entries(files).map(([path, source]) => {
  const id = path.split('/').pop()!.replace('.toml', '')
  return [id, { id, name: id.split('-').map((word) => word[0]!.toUpperCase() + word.slice(1)).join(' '), colors: parsePalette(source) }]
}))

export const activeTheme = shallowRef(themes.everforest!)

export function applyTheme(id: string, root: HTMLElement = window.document.documentElement) {
  const theme = themes[id] ?? themes.everforest!
  // Clear optional palette keys so they cannot leak from the previous theme.
  for (const key of Array.from(root.style)) {
    if (key.startsWith('--omarchy-')) root.style.removeProperty(key)
  }
  for (const [key, value] of Object.entries(theme.colors)) {
    if (key !== 'mode') root.style.setProperty(`--omarchy-${key.replaceAll('_', '-')}`, value)
  }
  activeTheme.value = theme
  root.dataset.theme = theme.id
  root.style.colorScheme = theme.colors.mode!
  return theme
}
