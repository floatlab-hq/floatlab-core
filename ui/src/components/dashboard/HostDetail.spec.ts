import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HostDetail from './HostDetail.vue'
import { hosts, apps } from '../../dashboard/fixtures'
import ApiForm from '../ApiForm.vue'
describe('Host detail', () => {
  it('shows the settings form and separates design-only preferences', async () => {
    const wrapper = mount(HostDetail, { props: { host: hosts[0]!, apps } })
    expect(wrapper.findAllComponents(ApiForm)).toHaveLength(2)
    expect(wrapper.text()).toContain('Design preview')
    wrapper.findComponent(ApiForm).vm.$emit('submit', { name: 'updated' })
    expect(wrapper.emitted('save')).toEqual([[{ name: 'updated' }]])
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.findAll('.host-detail-actions button')[0]!.trigger('click')
    expect(wrapper.emitted('action')).toEqual([['Pause']])
  })
  it('switches between apps, storage, logs and configuration', async () => {
    const wrapper = mount(HostDetail, { props: { host: hosts[0]!, apps } })
    for (const [label, content] of [['Apps', 'Apps on this host'], ['Storage', 'ZFS pool'], ['Logs', 'No log stream'], ['Config', 'Host settings']]) {
      await wrapper.findAll('[role="tab"]').find(tab => tab.text() === label)!.trigger('click')
      expect(wrapper.get('[role="tabpanel"]').text()).toContain(content)
    }
    expect(wrapper.findAllComponents(ApiForm)).toHaveLength(1)
  })
})
