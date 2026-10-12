import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DashboardWorkspace from './DashboardWorkspace.vue'
import { hosts, apps, events } from '../../dashboard/fixtures'
import LabDashboard from './LabDashboard.vue'
import HostDetail from './HostDetail.vue'
import ApiForm from '../ApiForm.vue'
describe('Dashboard workspace', () => {
  const props = { hosts, apps, events }
  it('opens host detail from the existing dashboard and returns on close', async () => {
    const wrapper = mount(DashboardWorkspace, { props })
    await wrapper.get('[aria-label="Inspect node-02"]').trigger('click')
    expect(wrapper.findComponent(HostDetail).props('host').name).toBe('node-02')
    wrapper.findComponent(HostDetail).vm.$emit('save', { name: 'renamed' })
    expect(wrapper.emitted('submit')).toEqual([['host', { name: 'renamed' }]])
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.findComponent(HostDetail).exists()).toBe(false)
    wrapper.findComponent(LabDashboard).vm.$emit('action', 'unhandled')
    expect(wrapper.emitted('action')).toContainEqual(['unhandled', undefined])
  })
  it('opens create and settings forms and emits local payloads', async () => {
    for (const [screen, schema] of [['add-host', 'NodeCreate'], ['add-app', 'StackCreate'], ['settings', 'AlertRuleCreate']]) {
      const wrapper = mount(DashboardWorkspace, { props })
      wrapper.findComponent(LabDashboard).vm.$emit('action', screen)
      await wrapper.vm.$nextTick()
      expect(wrapper.findComponent(ApiForm).props('schema').name).toBe(schema)
      wrapper.findComponent(ApiForm).vm.$emit('submit', { name: 'draft' })
      expect(wrapper.emitted('submit')).toEqual([[screen, { name: 'draft' }]])
      wrapper.unmount()
    }
  })
  it('supports an open initial panel and the global command palette', async () => {
    const wrapper = mount(DashboardWorkspace, { props: { ...props, initialPanel: 'add-host' } })
    expect(wrapper.findComponent(ApiForm).props('schema').name).toBe('NodeCreate')
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    await wrapper.get('.dashboard-command').trigger('click')
    expect(wrapper.find('dialog[open]').exists()).toBe(true)
  })
})
