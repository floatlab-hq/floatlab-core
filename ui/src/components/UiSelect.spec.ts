import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiSelect from './UiSelect.vue'
describe('Select', () => {
  const options = ['one', 'two'].map(value => ({ value, label: value }))
  it('emits the selected value and forwards native labels', async () => {
    const wrapper = mount(UiSelect, { props: { options }, attrs: { 'aria-label': 'Pool' } })
    await wrapper.setValue('two')
    expect(wrapper.emitted('update:modelValue')).toEqual([['two']])
    expect(wrapper.attributes('aria-label')).toBe('Pool')
  })
  it('supports multiple selections', async () => {
    const wrapper = mount(UiSelect, { props: { options, multiple: true, modelValue: [] } })
    await wrapper.setValue(['one', 'two'])
    expect(wrapper.emitted('update:modelValue')).toEqual([[['one', 'two']]])
  })
})
