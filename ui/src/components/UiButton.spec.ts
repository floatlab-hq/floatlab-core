import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UiButton from './UiButton.vue'

describe('UiButton', () => {
  it('renders its slot as a button by default', () => {
    const wrapper = mount(UiButton, { slots: { default: 'Save' } })

    expect(wrapper.text()).toBe('Save')
    expect(wrapper.attributes('type')).toBe('button')
    expect(wrapper.classes()).toEqual(['button'])
  })

  it.each(['plain', 'primary', 'outline', 'ghost', 'surface'] as const)('applies the %s variant', (variant) => {
    const wrapper = mount(UiButton, { props: { variant } })

    expect(wrapper.classes()).toEqual(['button', variant])
  })

  it('applies the small size', () => {
    const wrapper = mount(UiButton, { props: { size: 'small' } })

    expect(wrapper.classes()).toEqual(['button', 'small'])
  })

  it('passes native attributes, classes, and listeners to the button', async () => {
    const onClick = vi.fn()
    const wrapper = mount(UiButton, {
      attrs: { type: 'submit', class: 'search-trigger', 'aria-label': 'Search', onClick },
    })

    expect(wrapper.attributes()).toMatchObject({
      type: 'submit',
      class: 'button search-trigger',
      'aria-label': 'Search',
    })

    await wrapper.trigger('click')
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('passes the native disabled state to the button', () => {
    const wrapper = mount(UiButton, { attrs: { disabled: true } })

    expect(wrapper.attributes('disabled')).toBe('')
  })
})
