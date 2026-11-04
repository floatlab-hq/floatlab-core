import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiFlipCount from './UiFlipCount.vue'

describe('Flip count', () => {
  it('stays still initially and flips only when the count increases', async () => {
    const wrapper = mount(UiFlipCount, { props: { count: 0 } })
    expect(wrapper.find('.arriving').exists()).toBe(false)
    await wrapper.setProps({ count: 3 })
    const face = wrapper.get('.flip-count-face').element
    expect(wrapper.text()).toBe('3')
    expect(wrapper.find('.arriving').exists()).toBe(true)
    await wrapper.setProps({ count: 4 })
    expect(wrapper.get('.flip-count-face').element).not.toBe(face)
    const nextFace = wrapper.get('.flip-count-face').element
    await wrapper.setProps({ count: 2 })
    expect(wrapper.get('.flip-count-face').element).toBe(nextFace)
    expect(wrapper.text()).toBe('2')
    await wrapper.setProps({ count: 100 })
    expect(wrapper.text()).toBe('99+')
    expect(wrapper.attributes('aria-hidden')).toBe('true')
  })
})
