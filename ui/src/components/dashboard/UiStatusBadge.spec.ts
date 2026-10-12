import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UiStatusBadge from './UiStatusBadge.vue'


describe('Status badge', () => {
  it('represents transitional states and disables read-only actions', async () => {
    const badge = mount(UiStatusBadge, { props: { state: 'Updating', disabled: true } })
    expect(badge.text()).toBe('Updating')
    expect(badge.find('svg').exists()).toBe(true)
    expect(badge.attributes('aria-label')).toBe('Updating')
    await badge.trigger('click')
    expect(badge.emitted('click')).toBeUndefined()
  })
  it.each(['Running', 'Stopped', 'Failed', 'Starting'] as const)('renders %s with a native click', async (state) => {
    const onClick = vi.fn()
    const badge = mount(UiStatusBadge, { props: { state }, attrs: { onClick } })
    expect(badge.text()).toBe(state)
    expect(badge.classes()).toContain(state.toLowerCase())
    expect(badge.attributes('aria-label')).toContain(state)
    expect(badge.find('svg').exists()).toBe(true)
    await badge.trigger('click')
    expect(onClick).toHaveBeenCalledOnce()
  })
})
