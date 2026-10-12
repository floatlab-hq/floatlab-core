import { parse } from 'smol-toml'

export const requiredColors = [
  'accent', 'selection', 'muted', 'background', 'dark_background',
  'darker_background', 'lighter_background', 'foreground', 'dark_foreground',
  'light_foreground', 'bright_foreground', 'red', 'yellow', 'green', 'cyan',
  'blue', 'magenta', 'bright_red', 'bright_yellow', 'bright_green',
  'bright_cyan', 'bright_blue', 'bright_magenta',
] as const

export type Palette = Record<string, string>

export function parsePalette(source: string): Palette {
  const values = parse(source)
  // Omarchy still accepts the older bg/fg and ANSI color aliases.
  const aliases: Record<string, string> = {
    background: 'bg', dark_background: 'dark_bg', darker_background: 'darker_bg',
    lighter_background: 'lighter_bg', foreground: 'fg', dark_foreground: 'dark_fg',
    light_foreground: 'light_fg', bright_foreground: 'bright_fg',
    red: 'color1', green: 'color2', yellow: 'color3', blue: 'color4',
    magenta: 'color5', cyan: 'color6', muted: 'color8',
    bright_red: 'color9', bright_green: 'color10', bright_yellow: 'color11',
    bright_blue: 'color12', bright_magenta: 'color13', bright_cyan: 'color14',
  }
  for (const [key, alias] of Object.entries(aliases)) values[key] ??= values[alias]
  values.accent ??= values.blue
  values.selection ??= values.color8 ?? values.muted
  values.dark_background ??= values.background
  values.darker_background ??= values.dark_background
  values.lighter_background ??= values.selection
  values.dark_foreground ??= values.muted
  values.light_foreground ??= values.foreground
  values.bright_foreground ??= values.color15 ?? values.foreground
  for (const key of requiredColors) {
    if (typeof values[key] !== 'string' || !/^#[\da-f]{6}$/i.test(values[key])) {
      throw new Error(`Omarchy palette requires ${key} as a six-digit hex color`)
    }
  }
  if (values.mode !== undefined && values.mode !== 'light' && values.mode !== 'dark') {
    throw new Error('Omarchy mode must be light or dark')
  }
  if (values.mode === undefined) {
    const hex = values.background as string
    const brightness = [1, 3, 5].map((offset) => parseInt(hex.slice(offset, offset + 2), 16))
    values.mode = brightness[0]! * 0.299 + brightness[1]! * 0.587 + brightness[2]! * 0.114 > 128 ? 'light' : 'dark'
  }
  return Object.fromEntries(Object.entries(values).filter(([key, value]) =>
    typeof value === 'string' && (key === 'mode' || /^#[\da-f]{6}$/i.test(value)),
  )) as Palette
}
