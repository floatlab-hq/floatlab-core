import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DetailPanel from './DetailPanel.vue'
describe('Detail panel', () => {
  it('renders slots and closes with the button and Escape', async () => {
    const wrapper = mount(DetailPanel, { props: { title: 'Node' }, slots: { identity: '<span>Badge</span>', default: '<p>Fields</p>' }, attachTo: document.body })
    expect(wrapper.text()).toContain('Badge')
    expect(wrapper.text()).toContain('Fields')
    expect(document.activeElement).toBe(wrapper.element)
    await wrapper.get('button').trigger('click')
    await wrapper.trigger('keydown', { key: 'Escape' })
    expect(wrapper.emitted('close')).toHaveLength(2)
    wrapper.unmount()
  })
})
