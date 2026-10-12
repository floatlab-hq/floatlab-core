import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { apps } from '../data'
import AppTableRow from './AppTableRow.vue'
import ResourceBar from './ResourceBar.vue'
import ResourceTrack from './ResourceTrack.vue'
import StatusActionMenu from './StatusActionMenu.vue'

describe('AppTableRow', () => {
  it('renders all eight cells', () => {
    const wrapper = mount(AppTableRow, { props: { app: apps[0] } })
    const cells = wrapper.findAll('td')

    expect(cells).toHaveLength(8)
    expect(cells[0].text()).toContain('Immich')
    expect(cells[0].get('.app-icon').classes()).toContain('muted')
    expect(cells[1].text()).toContain('Starting')
    expect(cells[2].text()).toContain('caddy:8080')
    expect(wrapper.findAllComponents(ResourceBar).map((bar) => bar.text())).toEqual([
      '2%',
      '2.6 GB',
      '0.1 TB',
      '1%',
    ])
    expect(wrapper.findAllComponents(ResourceTrack)).toHaveLength(6)
    expect(cells[7].text()).toContain('↑ 5 MB')
    expect(cells[7].text()).toContain('↓ 1 MB')
  })

  it('renders error, warning, and default notices with links or an address', () => {
    const error = mount(AppTableRow, { props: { app: apps[1] } })
    const warning = mount(AppTableRow, { props: { app: apps[3] } })
    const normal = mount(AppTableRow, { props: { app: apps[0] } })

    expect(error.get('.links').text()).toBe('172.16.24.21')
    expect(error.get('button.inline-alert').text()).toBe('Exit Code 126: Permission Denied.')
    expect(warning.get('.warning').text()).toBe('Security Issue Found')
    expect(normal.get('td:nth-child(3) > div:last-child').text()).toBe('◉ Auto Updating in 3 days')
  })

  it('forwards operations from the status menu', () => {
    const wrapper = mount(AppTableRow, { props: { app: apps[0] } })

    wrapper.getComponent(StatusActionMenu).vm.$emit('operation', 'restart')

    expect(wrapper.emitted('operation')).toEqual([['restart']])
  })

  it('clamps both network bar widths', () => {
    const app = {
      ...apps[0],
      network: {
        up: { label: 'fast', percent: 140 },
        down: { label: 'idle', percent: -10 },
      },
    }
    const wrapper = mount(AppTableRow, { props: { app } })

    expect(
      wrapper.findAll<HTMLElement>('.row-network-bars b').map((bar) => bar.element.style.width),
    ).toEqual(['100%', '0%'])
  })
})
