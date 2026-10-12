import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import LiveHostDetail from './LiveHostDetail.vue'
import { node } from '../../dashboard/live-fixtures'
describe('Live host detail', () => {
  it('renders actual node configuration and unavailable storage/logs', async () => {
    const wrapper = mount(LiveHostDetail, { props: { node, apps: [] } })
    expect(wrapper.text()).toContain('alpha.local')
    expect(wrapper.text()).toContain('primary')
    const tabs = wrapper.findAll('[role="tab"]')
    await tabs[2]!.trigger('click')
    expect(wrapper.text()).toContain('Storage metrics unavailable')
    await tabs[3]!.trigger('click')
    expect(wrapper.text()).toContain('Logs unavailable')
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
