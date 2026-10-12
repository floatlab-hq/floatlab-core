import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiHealthMark from './UiHealthMark.vue'


describe('Health mark', () => {
  it.each(['ready', 'stopped', 'standby', 'degraded', 'unknown'] as const)('identifies %s without relying on color', (state) => {
    const mark = mount(UiHealthMark, { props: { state } })
    expect(mark.attributes('aria-label')).toBe(state)
    expect(mark.classes()).toContain(state)
    expect(mark.find('svg').exists()).toBe(state === 'standby')
  })
})
