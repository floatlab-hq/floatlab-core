import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiInput from './UiInput.vue'

describe('UiInput', () => {
  it('renders an empty text input by default', () => {
    const wrapper = mount(UiInput)

    expect(wrapper.classes()).toEqual(['ui-input'])
    expect(wrapper.element).toHaveProperty('value', '')
    expect(wrapper.attributes('type')).toBeUndefined()
  })

  it('supports the detail field variant', () => {
    expect(mount(UiInput, { props: { variant: 'field' } }).classes()).toContain('field')
  })

  it('updates v-model', async () => {
    const wrapper = mount(UiInput, { props: { modelValue: 'open' } })

    await wrapper.setValue('restart')

    expect(wrapper.emitted('update:modelValue')).toEqual([['restart']])
  })

  it('passes native attributes and classes to the input', () => {
    const wrapper = mount(UiInput, {
      attrs: {
        id: 'command-search',
        type: 'search',
        autofocus: true,
        disabled: true,
        placeholder: 'Search',
        class: 'command-search',
      },
    })

    expect(wrapper.attributes()).toMatchObject({
      id: 'command-search',
      type: 'search',
      autofocus: '',
      disabled: '',
      placeholder: 'Search',
      class: 'ui-input command-search',
    })
  })
})
