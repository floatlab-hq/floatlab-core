import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ResourceMetric from './ResourceMetric.vue'
import ResourceTrack from './ResourceTrack.vue'
import type { Metric } from '../types'

const metric = (id: Metric['id']): Metric => ({
  id,
  value: '77%',
  detail: '5.1 GHz',
  percent: 77,
  history: [74, 77, 76, 73],
})

describe('ResourceMetric', () => {
  it('renders the metric content and series', () => {
    const wrapper = mount(ResourceMetric, { props: { metric: metric('cpu') } })

    expect(wrapper.attributes('data-series')).toBe('cpu')
    expect(wrapper.get('.metric-meta strong').text()).toBe('77%')
    expect(wrapper.get('.metric-meta span').text()).toBe('5.1 GHz')
  })

  it.each([
    ['cpu', 'cpu-spec', ['14C/24T']],
    ['memory', 'ram-spec', ['/assets/ram.svg', '/assets/ram.svg']],
    [
      'storage',
      'storage-spec',
      ['/assets/storage-alert.svg', '/assets/storage-ok.svg', '/assets/storage-idle.svg'],
    ],
  ] as const)('renders the %s hardware badge', (id, badgeClass, contents) => {
    const wrapper = mount(ResourceMetric, { props: { metric: metric(id) } })
    const badge = wrapper.get(`.hardware-badge.${badgeClass}`)

    if (id === 'cpu') expect(badge.text()).toBe(contents[0])
    else expect(badge.findAll('img').map((image) => image.attributes('src'))).toEqual(contents)
  })

  it('omits the hardware badge for disk', () => {
    const wrapper = mount(ResourceMetric, { props: { metric: metric('disk') } })

    expect(wrapper.find('.hardware-badge').exists()).toBe(false)
  })

  it('passes the history and live percentage to ResourceTrack', () => {
    const value = { ...metric('cpu'), percent: 150, history: [-20, 25, 120, 75, 13] }
    const wrapper = mount(ResourceMetric, { props: { metric: value } })

    expect(wrapper.getComponent(ResourceTrack).props()).toMatchObject({
      percent: 150,
      history: [-20, 25, 120, 75, 13],
    })
  })
})
