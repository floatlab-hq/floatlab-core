import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { apps, server } from '../data'
import AppTable from './AppTable.vue'
import ServerPanel from './ServerPanel.vue'
import ServerSummary from './ServerSummary.vue'

describe('ServerPanel', () => {
  it('renders its children and forwards their events', () => {
    const wrapper = mount(ServerPanel, { props: { server, apps } })

    expect(wrapper.getComponent(ServerSummary).props('server')).toEqual(server)
    expect(wrapper.getComponent(AppTable).props('apps')).toEqual(apps)

    wrapper.getComponent(ServerSummary).vm.$emit('settings')
    wrapper.getComponent(AppTable).vm.$emit('operation', 'minecraft', 'restart')

    expect(wrapper.emitted('settings')).toEqual([[]])
    expect(wrapper.emitted('operation')).toEqual([['minecraft', 'restart']])
  })

  it('updates scroll-edge classes', async () => {
    const wrapper = mount(ServerPanel, { props: { server, apps } })
    const scroll = wrapper.get<HTMLElement>('.table-scroll')
    const section = wrapper.get('.server-panel')

    Object.defineProperties(scroll.element, {
      scrollLeft: { configurable: true, value: 0, writable: true },
      clientWidth: { configurable: true, value: 100 },
      scrollWidth: { configurable: true, value: 300 },
    })

    await scroll.trigger('scroll')
    expect(section.classes()).not.toContain('can-scroll-left')
    expect(section.classes()).toContain('can-scroll-right')

    scroll.element.scrollLeft = 200
    await scroll.trigger('scroll')
    expect(section.classes()).toContain('can-scroll-left')
    expect(section.classes()).not.toContain('can-scroll-right')
  })
})
