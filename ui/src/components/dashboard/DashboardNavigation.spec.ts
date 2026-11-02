import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DashboardNavigation from './DashboardNavigation.vue'

describe('Dashboard navigation', () => {
  it('places an interactive command field in the existing header box', () => {
    const wrapper = mount(DashboardNavigation, { slots: { command: '<input aria-label="Command" />' } })
    expect(wrapper.get('.dashboard-command-container').find('input').exists()).toBe(true)
    expect(wrapper.find('.dashboard-command').exists()).toBe(false)
  })
  it('offers the command palette and settings actions', async () => {
    const wrapper = mount(DashboardNavigation)
    expect(wrapper.get('a').attributes('aria-label')).toBe('FloatLab dashboard')
    await wrapper.get('.dashboard-command').trigger('click')
    await wrapper.get('.dashboard-settings').trigger('click')
    expect(wrapper.emitted('command')).toHaveLength(1)
    expect(wrapper.emitted('settings')).toHaveLength(1)
  })
})
