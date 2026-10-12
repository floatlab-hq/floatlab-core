import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { hosts, apps, events } from '../../dashboard/fixtures'
import LabDashboard from './LabDashboard.vue'
import DashboardNavigation from './DashboardNavigation.vue'
import HostDirectory from './HostDirectory.vue'
import AppDirectory from './AppDirectory.vue'
import LabEventLog from './LabEventLog.vue'
import CommandPalette from '../CommandPalette.vue'

const fixture = { hosts, apps, events }

describe('Lab dashboard', () => {
  it('uses proper heading levels, composes the patterns, and exposes filters', async () => {
    const wrapper = mount(LabDashboard, { props: fixture })
    expect(wrapper.get('h1').text()).toBe('Cavs lab')
    expect(wrapper.get('h2').text()).toBe('Apps')
    expect(wrapper.getComponent(HostDirectory).props('hosts')).toEqual(hosts)
    expect(wrapper.getComponent(AppDirectory).props('apps')).toEqual(apps)
    expect(wrapper.getComponent(LabEventLog).props('events')).toEqual(events)
    expect(wrapper.get('.dashboard-filter-note').text()).toContain('3 filters applied')
    await wrapper.get('.dashboard-filter-note button').trigger('click')
    expect(wrapper.emitted('action')).toContainEqual(['clear-filters'])
    await wrapper.setProps({ filterCount: 0 })
    expect(wrapper.find('.dashboard-filter-note').exists()).toBe(false)
  })

  it('opens, selects, and closes the existing command palette', async () => {
    const wrapper = mount(LabDashboard, { props: fixture })
    await wrapper.get('.dashboard-command').trigger('click')
    expect(wrapper.getComponent(CommandPalette).props('open')).toBe(true)
    expect(wrapper.get('dialog').attributes('open')).toBeDefined()
    wrapper.getComponent(CommandPalette).vm.$emit('select', 'add-host')
    wrapper.getComponent(CommandPalette).vm.$emit('close')
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('action')).toContainEqual(['add-host'])
    expect(wrapper.getComponent(CommandPalette).props('open')).toBe(false)
  })

  it('forwards actions from its child patterns', async () => {
    const wrapper = mount(LabDashboard, { props: fixture })
    wrapper.getComponent(DashboardNavigation).vm.$emit('settings')
    wrapper.getComponent(LabEventLog).vm.$emit('select', 'backup')
    const host = wrapper.getComponent(HostDirectory)
    host.vm.$emit('tools', 'node-01')
    host.vm.$emit('inspect', 'node-02')
    host.vm.$emit('connect', 'node-01', 'network')
    const app = wrapper.getComponent(AppDirectory)
    for (const event of ['state', 'tools', 'open', 'network']) app.vm.$emit(event, 'postgres')
    app.vm.$emit('columns')
    await wrapper.get('[aria-label="Filter apps"]').trigger('click')
    const buttons = wrapper.findAll('.dashboard-section-heading button')
    await buttons[0]!.trigger('click')
    await buttons[2]!.trigger('click')
    await wrapper.get('.dashboard-footer button').trigger('click')
    expect(wrapper.emitted('action')).toEqual([
      ['settings'], ['event', 'backup'], ['host-tools', 'node-01'], ['host-inspect', 'node-02'],
      ['host-network', 'node-01'], ['app-state', 'postgres'], ['app-tools', 'postgres'],
      ['app-open', 'postgres'], ['app-network', 'postgres'], ['columns'], ['filters'],
      ['add-host'], ['add-app'], ['node-key'],
    ])
  })
})
