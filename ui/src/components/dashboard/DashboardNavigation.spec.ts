import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DashboardNavigation from './DashboardNavigation.vue'

describe('Dashboard navigation', () => {
  it('places an interactive command field in the existing header box', () => {
    const wrapper = mount(DashboardNavigation, { slots: { command: '<input aria-label="Command" />' } })
    expect(wrapper.get('.dashboard-command-container').find('input').exists()).toBe(true)
    expect(wrapper.find('.dashboard-command').exists()).toBe(false)
  })
  it('summarizes severity, opens events, and exposes the active state', async () => {
    const wrapper = mount(DashboardNavigation)
    const events = wrapper.get('.dashboard-events')
    expect(events.attributes('aria-label')).toBe('Events: 0 recent errors, 0 recent warnings')
    expect(events.classes()).not.toContain('has-errors')
    await wrapper.setProps({ warningCount: 2 })
    expect(events.classes()).toContain('has-warnings')
    expect(events.text()).toContain('2')
    await wrapper.setProps({ errorCount: 3, eventsOpen: true })
    expect(events.classes()).toContain('has-errors')
    expect(events.classes()).not.toContain('has-warnings')
    expect(events.attributes('aria-expanded')).toBe('true')
    await events.trigger('click')
    expect(wrapper.emitted('events')).toHaveLength(1)
  })
  it('offers the command palette and settings actions', async () => {
    const wrapper = mount(DashboardNavigation)
    expect(wrapper.get('a').attributes('aria-label')).toBe('FloatLab dashboard')
    await wrapper.get('.dashboard-command').trigger('click')
    await wrapper.get('.dashboard-settings').trigger('click')
    expect(wrapper.emitted('command')).toHaveLength(1)
    expect(wrapper.get('.dashboard-settings').attributes('aria-expanded')).toBe('true')
    await wrapper.findAll('.dashboard-settings-dropdown button')[0]!.trigger('click')
    expect(wrapper.emitted('settings')).toHaveLength(1)
    expect(wrapper.find('.dashboard-settings-dropdown').exists()).toBe(false)
  })
  it('opens account actions, dismisses outside and on Escape, and restores trigger focus', async () => {
    const wrapper = mount(DashboardNavigation, { attachTo: document.body })
    const trigger = wrapper.get('.dashboard-settings')
    for (const [index, event] of [[1, 'nodeKey'], [2, 'logout']] as const) {
      await trigger.trigger('click')
      await wrapper.findAll('.dashboard-settings-dropdown button')[index]!.trigger('click')
      expect(wrapper.emitted(event)).toHaveLength(1)
      expect(wrapper.find('.dashboard-settings-dropdown').exists()).toBe(false)
    }
    await trigger.trigger('click')
    await wrapper.get('.dashboard-settings-dropdown').trigger('keydown', { key: 'Escape' })
    expect(document.activeElement).toBe(trigger.element)
    expect(wrapper.find('.dashboard-settings-dropdown').exists()).toBe(false)
    await trigger.trigger('click')
    await wrapper.get('.dashboard-settings-container').trigger('focusout', { relatedTarget: trigger.element })
    expect(wrapper.find('.dashboard-settings-dropdown').exists()).toBe(true)
    await wrapper.get('.dashboard-settings-container').trigger('focusout', { relatedTarget: document.body })
    expect(wrapper.find('.dashboard-settings-dropdown').exists()).toBe(false)
    await trigger.trigger('click')
    await trigger.trigger('pointerdown')
    expect(wrapper.find('.dashboard-settings-dropdown').exists()).toBe(true)
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.dashboard-settings-dropdown').exists()).toBe(false)
    wrapper.unmount()
  })

})
