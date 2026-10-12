import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import StatusActionMenu from './StatusActionMenu.vue'

function mountMenu(pendingOperation?: 'start' | 'restart' | 'stop') {
  return mount(StatusActionMenu, { props: { status: 'Running', pendingOperation } })
}

describe('StatusActionMenu', () => {
  it.each([
    ['Starting', 'starting'],
    ['Failed', 'error'],
    ['Restarting', 'restarting'],
    ['Running', 'running'],
  ] as const)('maps %s to the %s status class', (status, statusClass) => {
    const wrapper = mount(StatusActionMenu, { props: { status } })

    expect(wrapper.get('.status').classes()).toContain(statusClass)
  })

  it('toggles the menu', async () => {
    const wrapper = mountMenu()
    const trigger = wrapper.get('.status')

    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    expect(wrapper.find('.status-menu').exists()).toBe(true)
    expect(trigger.attributes('aria-expanded')).toBe('true')
    await trigger.trigger('click')
    expect(wrapper.find('.status-menu').exists()).toBe(false)
  })

  it.each([
    ['start', 0],
    ['restart', 1],
    ['stop', 2],
  ] as const)('disables the pending %s operation', async (operation, index) => {
    const wrapper = mountMenu(operation)

    await wrapper.get('.status').trigger('click')

    expect(wrapper.findAll('.status-menu button')[index].attributes()).toHaveProperty('disabled')
  })

  it('emits the selected operation and closes', async () => {
    const wrapper = mountMenu()

    await wrapper.get('.status').trigger('click')
    await wrapper.findAll('.status-menu button')[2].trigger('click')

    expect(wrapper.emitted('operation')).toEqual([['stop']])
    expect(wrapper.find('.status-menu').exists()).toBe(false)
  })

  it('closes on Escape', async () => {
    const wrapper = mountMenu()

    await wrapper.get('.status').trigger('click')
    await wrapper.trigger('keydown', { key: 'Escape' })

    expect(wrapper.find('.status-menu').exists()).toBe(false)
  })
})
