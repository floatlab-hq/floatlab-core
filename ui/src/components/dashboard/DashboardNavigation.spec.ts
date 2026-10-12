import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DashboardNavigation from './DashboardNavigation.vue'

describe('Dashboard navigation', () => {
  it('offers the command palette and settings actions', async () => {
    const wrapper = mount(DashboardNavigation)
    expect(wrapper.get('a').attributes('aria-label')).toBe('FloatLab dashboard')
    await wrapper.get('.dashboard-command').trigger('click')
    await wrapper.get('.dashboard-settings').trigger('click')
    expect(wrapper.emitted('command')).toHaveLength(1)
    expect(wrapper.emitted('settings')).toHaveLength(1)
  })
})
