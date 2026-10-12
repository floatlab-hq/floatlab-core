import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DashboardHeader from './DashboardHeader.vue'

describe('DashboardHeader', () => {
  it('renders build details', () => {
    const wrapper = mount(DashboardHeader, { props: { version: 'v1.43', uptime: '24d' } })

    expect(wrapper.find('.build-meta').text()).toBe('v1.43Uptime 24d')
  })

  it('emits each header action', async () => {
    const wrapper = mount(DashboardHeader, { props: { version: 'v1.43', uptime: '24d' } })

    await wrapper.find('.search-trigger').trigger('click')
    await wrapper.find('.alerts').trigger('click')
    await wrapper.find('.primary').trigger('click')

    expect(wrapper.emitted('open-command-palette')).toHaveLength(1)
    expect(wrapper.emitted('show-alerts')).toHaveLength(1)
    expect(wrapper.emitted('create-app')).toHaveLength(1)
  })
})
