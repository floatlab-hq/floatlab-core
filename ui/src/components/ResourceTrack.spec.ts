import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ResourceTrack from './ResourceTrack.vue'

describe('ResourceTrack', () => {
  it.each([
    [-10, '0%'],
    [40, '40%'],
    [120, '100%'],
  ])('clamps %s to %s', (percent, width) => {
    const wrapper = mount(ResourceTrack, { props: { percent } })

    expect(wrapper.get<HTMLElement>('b').element.style.width).toBe(width)
    expect(wrapper.classes()).not.toContain('danger')
  })

  it('renders the compact usage meter', () => {
    const wrapper = mount(ResourceTrack, { props: { percent: 38, compact: true } })
    expect(wrapper.classes()).toContain('compact')
    expect(wrapper.get<HTMLElement>('b').element.style.width).toBe('38%')
  })

  it('renders the danger state', () => {
    expect(mount(ResourceTrack, { props: { percent: 50, danger: true } }).classes()).toContain(
      'danger',
    )
  })

  it('renders and clamps four history values', () => {
    const wrapper = mount(ResourceTrack, {
      props: { percent: 50, history: [-10, 25, 120, 75, 13] },
    })

    expect(wrapper.get<HTMLElement>('.history').element.style.clipPath).toBe(
      'polygon(0 0, 0% 0, 0% 25%, 25% 25%, 25% 50%, 100% 50%, 100% 75%, 75% 75%, 75% 100%, 0 100%)',
    )
    expect(wrapper.get<HTMLElement>('.live-track b').element.style.width).toBe('50%')
  })

  it('defaults missing history values to zero and supports reverse', () => {
    const wrapper = mount(ResourceTrack, { props: { percent: 50, history: [20], reverse: true } })

    expect(wrapper.classes()).toContain('reverse')
    expect(wrapper.get<HTMLElement>('.history').element.style.clipPath).toContain(
      '20% 0, 20% 25%, 0% 25%',
    )
  })
})
