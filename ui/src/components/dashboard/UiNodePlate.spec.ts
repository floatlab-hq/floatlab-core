import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiNodePlate from './UiNodePlate.vue'
import { applyTheme } from '../../themes'
import { nodeCode } from '../../dashboard/node-code'

function geometry(uri: string) {
  return decodeURIComponent(uri.split(',')[1]!).replace(/#[a-f\d]{6}/gi, '#COLOR')
}

describe('Node plate', () => {
  it.each([
    ['node-01', 'ND1'], ['Everforest', 'VRT'], ['Death Star', 'DTR'],
    ['café', 'CFE'], ['Bob', 'BBB'], ['Ae', 'AEE'], ['A', 'AAA'],
    ['123', '123'], [' -- ', '—'],
  ])('abbreviates %s as %s', (name, expected) => {
    expect(nodeCode(name)).toBe(expected)
  })

  it('uses the full name for repeatable geometry and accessible identification', async () => {
    const plate = mount(UiNodePlate, { props: { name: 'node-01' } })
    const duplicate = mount(UiNodePlate, { props: { name: 'node-01' } })
    expect(plate.attributes('aria-label')).toBe('Node node-01')
    expect(plate.text()).toBe('ND1')
    expect(plate.get('img').attributes('src')!).toBe(duplicate.get('img').attributes('src')!)
    const original = plate.get('img').attributes('src')!
    await plate.setProps({ name: 'node-02' })
    expect(plate.text()).toBe('ND2')
    expect(geometry(plate.get('img').attributes('src')!)).not.toBe(geometry(original))
  })

  it('recolors the shape across themes without changing geometry', async () => {
    applyTheme('everforest')
    const plate = mount(UiNodePlate, { props: { name: 'node-01' } })
    const original = plate.get('img').attributes('src')!
    applyTheme('nord')
    await plate.vm.$nextTick()
    expect(plate.get('img').attributes('src')!).not.toBe(original)
    expect(geometry(plate.get('img').attributes('src')!)).toBe(geometry(original))
    applyTheme('everforest')
  })
})
