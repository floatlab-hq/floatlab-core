import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ResourceTrack from './ResourceTrack.vue'
import ServerSummary from './ServerSummary.vue'
import UiButton from './UiButton.vue'
import type { Server } from '../types'

const server: Server = {
  name: 'Cavs Server',
  temperature: '45°C',
  uptime: '35 days',
  metrics: [
    { id: 'cpu', value: '77%', detail: '5.1 GHz', percent: 77, history: [74, 77, 76, 73] },
    { id: 'memory', value: '56%', detail: '128 GB', percent: 56, history: [55, 55, 55, 55] },
    { id: 'storage', value: '53%', detail: '14 TB', percent: 53, history: [53, 53, 53, 53] },
    { id: 'disk', value: '35%', detail: '24 GB', percent: 35, history: [41, 47, 41, 34] },
  ],
  network: {
    up: { label: '148 MB', percent: 120 },
    down: { label: '11 MB', percent: -10 },
  },
}

describe('ServerSummary', () => {
  it('renders the server identity, metrics, and network', () => {
    const wrapper = mount(ServerSummary, { props: { server } })

    expect(wrapper.get('h1').text()).toBe('Cavs Server')
    expect(wrapper.get('svg.server-mark').attributes('aria-hidden')).toBe('true')
    expect(wrapper.get('.server-identity p').text()).toContain('45°C')
    expect(wrapper.get('.server-identity p').text()).toContain('35 days')
    expect(wrapper.findAllComponents({ name: 'ResourceMetric' })).toHaveLength(4)
    expect(wrapper.get('.network-values').text()).toContain('148 MB')
    expect(wrapper.get('.network-values').text()).toContain('11 MB')
    expect(wrapper.findAll('.network-spec img').map((image) => image.attributes('src'))).toEqual([
      '/assets/network-port-idle.svg',
      '/assets/network-port-active.svg',
    ])
    expect(
      wrapper
        .findAllComponents(ResourceTrack)
        .filter((track) => track.attributes('data-series'))
        .map((track) => [track.attributes('data-series'), track.props()]),
    ).toEqual([
      ['networkUp', { percent: 120, history: [], reverse: true, danger: false, compact: false }],
      ['networkDown', { percent: -10, history: [], reverse: false, danger: false, compact: false }],
    ])
  })

  it('emits settings from the Settings button', async () => {
    const wrapper = mount(ServerSummary, { props: { server } })

    const button = wrapper.getComponent(UiButton)

    expect(button.props()).toMatchObject({ variant: 'outline', size: 'small' })
    await button.trigger('click')

    expect(wrapper.emitted('settings')).toHaveLength(1)
  })
})
