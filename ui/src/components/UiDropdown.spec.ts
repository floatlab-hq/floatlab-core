import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import UiDropdown from './UiDropdown.vue'

enableAutoUnmount(afterEach)
function dropdown(items = [{ id: 'stop', label: 'Stop' }, { id: 'restart', label: 'Restart' }, { id: 'disabled', label: 'Unavailable', disabled: true }]) {
  return mount(UiDropdown, { attachTo: document.body, props: { label: 'Actions', items }, slots: { trigger: '<template #trigger="{ toggle, open, controls }"><button :aria-expanded="open" :aria-controls="controls" @click="toggle">Open</button></template>' } })
}
describe('Dropdown', () => {
  it('teleports outside clipping containers, selects once, and restores focus', async () => {
    const wrapper = dropdown()
    await wrapper.get('button').trigger('click'); await flushPromises()
    const menu = document.querySelector<HTMLElement>('[role="menu"]')!
    expect(menu.parentElement).toBe(document.body)
    expect(menu.getAttribute('aria-label')).toBe('Actions')
    expect(document.activeElement?.textContent).toBe('Stop')
    menu.querySelectorAll('button')[1]!.click()
    await flushPromises()
    expect(wrapper.emitted('select')).toEqual([['restart']])
    expect(document.querySelector('[role="menu"]')).toBeNull()
    expect(document.activeElement).toBe(wrapper.get('button').element)
  })
  it('supports arrow keys, Home, End, Escape, and Tab', async () => {
    const wrapper = dropdown()
    await wrapper.get('button').trigger('click'); await flushPromises()
    const menu = document.querySelector('[role="menu"]')!
    for (const [key, label] of [['ArrowUp', 'Restart'], ['ArrowDown', 'Stop'], ['End', 'Restart'], ['Home', 'Stop']]) {
      menu.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
      expect(document.activeElement?.textContent).toBe(label)
    }
    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(wrapper.get('button').attributes('aria-expanded')).toBe('false')
    await wrapper.get('button').trigger('click'); await flushPromises()
    document.querySelector('[role="menu"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))
    await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('button').element)
  })
  it('dismisses outside, on resize, and by toggling without emitting an action', async () => {
    const wrapper = dropdown()
    await wrapper.get('button').trigger('click'); await flushPromises()
    document.querySelector('[role="menu"]')!.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(document.querySelector('[role="menu"]')).not.toBeNull()
    await wrapper.get('button').trigger('click')
    expect(document.querySelector('[role="menu"]')).toBeNull()
    await wrapper.get('button').trigger('click'); await flushPromises()
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(document.querySelector('[role="menu"]')).toBeNull()
    await wrapper.get('button').trigger('click'); await flushPromises()
    window.dispatchEvent(new Event('resize')); await flushPromises()
    expect(document.querySelector('[role="menu"]')).toBeNull()
    expect(wrapper.emitted('select')).toBeUndefined()
  })
  it('places menus above low triggers and leaves disabled choices inactive', async () => {
    const wrapper = dropdown([{ id: 'none', label: 'Unavailable', disabled: true }])
    vi.spyOn(wrapper.get('.dropdown-trigger').element, 'getBoundingClientRect').mockReturnValue({ top: 700, bottom: 732, left: 950 } as DOMRect)
    await wrapper.get('button').trigger('click'); await flushPromises()
    const menu = document.querySelector<HTMLElement>('[role="menu"]')!
    expect(parseInt(menu.style.top)).toBeLessThan(700)
    expect(parseInt(menu.style.left)).toBeLessThanOrEqual(window.innerWidth - 240)
    menu.querySelector('button')!.click()
    expect(wrapper.emitted('select')).toBeUndefined()
  })
})
