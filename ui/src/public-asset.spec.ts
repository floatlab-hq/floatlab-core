import { describe, expect, it } from 'vitest'
import { publicAsset } from './public-asset'
describe('Public asset paths', () => {
  it('keeps local root assets and prefixes GitHub Pages assets', () => {
    expect(publicAsset('/assets/logo.svg', '/')).toBe('/assets/logo.svg')
    expect(publicAsset('/assets/logo.svg', '/floatlab-ui/')).toBe('/floatlab-ui/assets/logo.svg')
  })
  it('preserves relative, external and generated image URLs', () => {
    for (const path of ['assets/logo.svg', 'https://example.com/logo.svg', '//example.com/logo.svg', 'data:image/svg+xml,svg']) expect(publicAsset(path, '/floatlab-ui/')).toBe(path)
  })
})
