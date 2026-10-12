import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@floatlab/client'
import LiveDashboard from './LiveDashboard.vue'
import { fixtureApi } from '../../dashboard/live-fixtures'
enableAutoUnmount(afterEach)
afterEach(() => vi.useRealTimers())
describe('Live dashboard', () => {
  it('loads the API and opens host detail with recent logs', async () => {
    const wrapper = mount(LiveDashboard, { props: { api: fixtureApi() } })
    await flushPromises()
    expect(wrapper.text()).toContain('lab-alpha')
    expect(wrapper.text()).toContain('plex is running')
    expect(wrapper.text()).toContain('Leader · 1 peers')
    expect(wrapper.text()).not.toContain('quorum healthy')
    await wrapper.get('[aria-label="Inspect lab-alpha"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[aria-label="lab-alpha"]').text()).toContain('alpha.local')
    await wrapper.findAll('[role="tab"]')[3]!.trigger('click')
    expect(wrapper.text()).toContain('Host connected')
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.find('[aria-label="lab-alpha"]').exists()).toBe(false)
  })
  it('keeps the dashboard available when metrics fail', async () => {
    const api = fixtureApi()
    api.getNodeStats = vi.fn().mockRejectedValue(new ApiError('Metrics down', 503))
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Metrics down')
    expect(wrapper.text()).toContain('lab-alpha')
    expect(wrapper.find('[aria-label="Memory unavailable"]').exists()).toBe(true)
  })
  it('returns to login on unauthorized requests', async () => {
    const api = fixtureApi()
    api.listNodes = vi.fn().mockRejectedValue(new ApiError('Expired', 401))
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    expect(wrapper.emitted('logout')).toHaveLength(1)
  })
  it('refreshes on the interval and cleans up on unmount', async () => {
    vi.useFakeTimers()
    const api = fixtureApi()
    const list = vi.spyOn(api, 'listNodes')
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(list).toHaveBeenCalledTimes(2)
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(list).toHaveBeenCalledTimes(2)
  })
})
