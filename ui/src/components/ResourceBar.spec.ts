import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ResourceBar from './ResourceBar.vue'
import ResourceTrack from './ResourceTrack.vue'

describe('ResourceBar', () => {
  it('renders the label and danger class', () => {
    const wrapper = mount(ResourceBar, {
      props: { value: { label: '82 MB', percent: 42 }, danger: true },
    })

    expect(wrapper.get('.cell-metric').text()).toBe('82 MB')
    expect(wrapper.get('.cell-metric').classes()).toContain('danger')
    expect(wrapper.getComponent(ResourceTrack).props()).toMatchObject({ percent: 42, danger: true })
  })

  it.each([
    [-10, '0%'],
    [40, '40%'],
    [120, '100%'],
  ])('clamps %s to %s', (percent, width) => {
    const wrapper = mount(ResourceBar, { props: { value: { label: 'Usage', percent } } })

    expect(wrapper.get<HTMLElement>('b').element.style.width).toBe(width)
    expect(wrapper.get('.cell-metric').classes()).not.toContain('danger')
  })
})
