import { describe, expect, it } from 'vitest'
import { activeTheme, applyTheme, themes } from './index'
import { parsePalette, requiredColors } from './parse-palette'
import source from './source.json'

const everforest = import.meta.glob('./palettes/everforest.toml', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const paletteSource = Object.values(everforest)[0]!

describe('Omarchy themes', () => {
  it('contains every pinned official palette and preserves required hex colors', () => {
    expect(Object.keys(themes)).toEqual(expect.arrayContaining(source.themes))
    for (const theme of Object.values(themes)) {
      for (const key of requiredColors) expect(theme.colors[key]).toMatch(/^#[\da-f]{6}$/i)
    }
    expect(themes.everforest!.colors.background).toBe('#2d353b')
  })

  it('switches every palette, sets native color scheme, and removes optional colors', () => {
    const root = window.document.createElement('div')
    for (const theme of Object.values(themes)) {
      root.style.setProperty('--omarchy-previous-color', '#123456')
      applyTheme(theme.id, root)
      expect(root.dataset.theme).toBe(theme.id)
      expect(activeTheme.value.id).toBe(theme.id)
      expect(root.style.colorScheme).toBe(theme.colors.mode)
      expect(root.style.getPropertyValue('--omarchy-previous-color')).toBe('')
      for (const key of requiredColors) {
        expect(root.style.getPropertyValue(`--omarchy-${key.replaceAll('_', '-')}`)).toBe(theme.colors[key])
      }
    }
    applyTheme('unknown', root)
    expect(root.dataset.theme).toBe('everforest')
  })

  it('applies Everforest to the document by default', () => {
    applyTheme('everforest')
    expect(window.document.documentElement.dataset.theme).toBe('everforest')
  })

  it.each(requiredColors)('rejects invalid %s', (key) => {
    expect(() => parsePalette(paletteSource.replace(new RegExp(`^${key} = .*`, 'm'), `${key} = "invalid"`))).toThrow(key)
  })

  it('rejects invalid TOML and modes', () => {
    expect(() => parsePalette('broken = [')).toThrow()
    expect(() => parsePalette(paletteSource.replace('mode = "dark"', 'mode = "invalid"'))).toThrow('mode')
  })

  it.each(['everforest', 'white'])('infers %s mode when absent', (name) => {
    const theme = themes[name]!
    const text = Object.entries(theme.colors).filter(([key]) => key !== 'mode').map(([key, value]) => `${key} = "${value}"`).join('\n')
    expect(parsePalette(text).mode).toBe(theme.colors.mode)
  })

  it('normalizes legacy ANSI and short aliases with canonical names taking precedence', () => {
    const ansi = Array.from({ length: 16 }, (_, index) => `color${index} = "#123456"`).join('\n')
    const parsed = parsePalette(`bg = "#111111"\nfg = "#eeeeee"\n${ansi}`)
    expect(parsed.background).toBe('#111111')
    expect(parsed.foreground).toBe('#eeeeee')
    expect(parsed.accent).toBe('#123456')
    expect(parsed.dark_background).toBe('#111111')
    expect(parsed.bright_foreground).toBe('#123456')
    expect(parsePalette(`${paletteSource}\nbg = "#000000"`).background).toBe('#2d353b')
  })
})
