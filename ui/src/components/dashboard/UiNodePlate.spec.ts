import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiNodePlate from './UiNodePlate.vue'
import { nodeCode } from '../../dashboard/node-code'

describe('Node plate', () => {
  it.each([
    ['node-01', 'ND1'], ['Everforest', 'VRT'], ['Death Star', 'DTR'],
    ['café', 'CFE'], ['Bob', 'BBB'], ['Ae', 'AEE'], ['A', 'AAA'],
    ['123', '123'], [' -- ', '—'],
  ])('abbreviates %s as %s', (name, expected) => {
    expect(nodeCode(name)).toBe(expected)
  })

  it('shows only the host code with the full name available accessibly', async () => {
    const plate = mount(UiNodePlate, { props: { name: 'node-01' } })
    expect(plate.attributes('aria-label')).toBe('Node node-01')
    expect(plate.attributes('title')).toBe('node-01')
    expect(plate.text()).toBe('ND1')
    expect(plate.find('img, svg').exists()).toBe(false)
    await plate.setProps({ name: 'node-02' })
    expect(plate.text()).toBe('ND2')
    expect(plate.attributes('aria-label')).toBe('Node node-02')
  })

  it('uses a native button and edit affordance when interactive', async () => {
    const plate = mount(UiNodePlate, { props: { name: 'node-01', editable: true } })
    expect(plate.element.tagName).toBe('BUTTON')
    expect(plate.attributes('aria-label')).toBe('Change host node-01')
    expect(plate.find('.node-edit-icon').exists()).toBe(true)
    await plate.trigger('click')
    expect(plate.emitted('click')).toHaveLength(1)
  })

})
