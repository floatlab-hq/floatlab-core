import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiSwitch from './UiSwitch.vue'
describe('Switch', () => {
  it('announces and toggles its explicit state', async () => {
    const wrapper = mount(UiSwitch, { props: { label: 'Alerts', modelValue: false } })
    expect(wrapper.attributes('role')).toBe('switch')
    expect(wrapper.attributes('aria-checked')).toBe('false')
    expect(wrapper.text()).toBe('Off')
    await wrapper.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[true]])
    await wrapper.setProps({ modelValue: true })
    expect(wrapper.text()).toBe('On')
  })
  it('does not toggle when disabled', async () => {
    const wrapper = mount(UiSwitch, { props: { label: 'Alerts', disabled: true } })
    await wrapper.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
