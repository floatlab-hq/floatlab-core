import { readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { parsePalette } from '../src/themes/parse-palette.ts'

const [name, source] = process.argv.slice(2)
if (!name || !source || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(name)) {
  throw new Error('Usage: pnpm theme:add <theme-name> <local colors.toml | HTTPS URL | GitHub repo URL>')
}
let url = source
if (/^https:\/\/github\.com\/[^/]+\/[^/]+\/?$/.test(source)) {
  url = source.replace('https://github.com/', 'https://raw.githubusercontent.com/').replace(/\/$/, '').replace(/\.git$/, '') + '/HEAD/colors.toml'
}
let contents: string
if (url.startsWith('https://')) {
  const response = await fetch(url)
  if (!response.ok) throw new Error(`Palette download failed: ${response.status}`)
  contents = await response.text()
} else {
  contents = await readFile(source, 'utf8')
}
parsePalette(contents)
const target = new URL(`../src/themes/palettes/${name}.toml`, import.meta.url)
await writeFile(target, contents, { flag: 'wx' })
console.log(`Added ${name} at ${fileURLToPath(target)}. Restart Storybook to update the theme menu.`)
