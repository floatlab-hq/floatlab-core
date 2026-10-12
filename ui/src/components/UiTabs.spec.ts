import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiTabs from './UiTabs.vue'
describe('Tabs', () => {
  const tabs = [{ id: 'a', label: 'A' }, { id: 'disabled', label: 'Disabled', disabled: true }, { id: 'b', label: 'B' }]
  it('connects selected tabs to the panel and emits clicks', async () => {
    const wrapper = mount(UiTabs, { props: { tabs, modelValue: 'a', label: 'Host' }, slots: { default: '<p>Panel content</p>' } })
    expect(wrapper.get('[role="tabpanel"]').attributes('aria-labelledby')).toBe(wrapper.findAll('button')[0]!.attributes('id'))
    await wrapper.findAll('button')[2]!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['b']])
    expect(wrapper.text()).toContain('Panel content')
  })
  it('navigates and wraps with the keyboard, skipping disabled tabs', async () => {
    for (const [key, value] of [['ArrowRight', 'b'], ['ArrowLeft', 'b'], ['End', 'b'], ['Home', 'a']]) {
      const wrapper = mount(UiTabs, { props: { tabs, modelValue: 'a', label: 'Host' } })
      await wrapper.findAll('button')[0]!.trigger('keydown', { key })
      if (value === 'a') expect(wrapper.emitted('update:modelValue')).toBeUndefined()
      else expect(wrapper.emitted('update:modelValue')).toEqual([[value]])
      wrapper.unmount()
    }
    const wrapper = mount(UiTabs, { props: { tabs, modelValue: 'a', label: 'Host' } })
    await wrapper.findAll('button')[0]!.trigger('keydown', { key: 'Tab' })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
