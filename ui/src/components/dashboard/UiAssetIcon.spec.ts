import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiAssetIcon from './UiAssetIcon.vue'


describe('Asset icon', () => {
  it('keeps vector silhouettes local and uses currentColor', () => {
    const icon = mount(UiAssetIcon, { props: { src: '/assets/dashboard/imgFloatLabLogo.svg' } })
    expect(icon.attributes('style')).toContain('mask-image: url("/assets/dashboard/imgFloatLabLogo.svg")')
    expect(icon.attributes('style')).toContain('width: 20px')
    expect(icon.attributes('aria-hidden')).toBe('true')
  })
  it('supports a requested size', () => {
    const icon = mount(UiAssetIcon, { props: { src: '/logo.svg', size: 34 } })
    expect(icon.attributes('style')).toContain('width: 34px')
  })
})
