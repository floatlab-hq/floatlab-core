import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import SplitView from './SplitView.vue'
describe('Split view', () => {
  it('notifies consumers when the closing transition finishes', async () => {
    const wrapper = mount(SplitView, { props: { open: true }, global: { stubs: { transition: false } } })
    await wrapper.setProps({ open: false })
    await vi.waitFor(() => expect(wrapper.emitted('closed')).toEqual([[]]))
  })
  it('keeps primary content and mounts detail only while open', async () => {
    const wrapper = mount(SplitView, { slots: { default: '<p>Main</p>', detail: '<p>Detail</p>' } })
    expect(wrapper.text()).toBe('Main')
    await wrapper.setProps({ open: true })
    expect(wrapper.classes()).toContain('is-open')
    expect(wrapper.text()).toContain('Detail')
    await wrapper.setProps({ open: false })
    expect(wrapper.find('.split-detail').exists()).toBe(false)
  })
})
