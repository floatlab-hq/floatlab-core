import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiTextarea from './UiTextarea.vue'
describe('Textarea', () => {
  it('edits multiline values and preserves native field attributes', async () => {
    const wrapper = mount(UiTextarea, { props: { modelValue: 'services: {}' }, attrs: { 'aria-label': 'Compose file', required: true, rows: 6 } })
    expect(wrapper.element.value).toBe('services: {}')
    expect(wrapper.attributes('required')).toBe('')
    expect(wrapper.attributes('rows')).toBe('6')
    await wrapper.setValue('services:\n  db: {}')
    expect(wrapper.emitted('update:modelValue')).toEqual([['services:\n  db: {}']])
  })
  it('defaults to empty and accepts a disabled state', () => {
    const wrapper = mount(UiTextarea, { attrs: { disabled: true } })
    expect(wrapper.element.value).toBe('')
    expect(wrapper.element.disabled).toBe(true)
  })
})
