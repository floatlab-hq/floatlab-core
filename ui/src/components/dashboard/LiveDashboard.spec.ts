import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@floatlab/client'
import LiveDashboard from './LiveDashboard.vue'
import { fixtureApi, interactiveFixtureApi } from '../../dashboard/live-fixtures'
import CreateApp from './CreateApp.vue'
import WorkloadDetail from './WorkloadDetail.vue'
import CommandConsole from '../CommandConsole.vue'
import LabDashboard from './LabDashboard.vue'
enableAutoUnmount(afterEach)
afterEach(() => vi.useRealTimers())
describe('Live dashboard', () => {
  it('renders through the Lab Dashboard story component and opens the node key', async () => {
    const wrapper = mount(LiveDashboard, { props: { api: fixtureApi() } })
    await flushPromises()
    const dashboard = wrapper.getComponent(LabDashboard)
    expect(dashboard.props()).toMatchObject({ live: true, labName: 'My lab', clusterStatus: 'Leader · 1 peers' })
    expect(wrapper.get('.dashboard-footer').text()).toContain('Sign out')
    expect(wrapper.find('[aria-label="Backup destinations"]').exists()).toBe(false)
    await wrapper.findAll('button').find(button => button.text().startsWith('Node key'))!.trigger('click')
    expect(wrapper.get('[aria-label="Node key"]').text()).toContain('lab-alpha')
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.find('[aria-label="Node key"]').exists()).toBe(false)
  })
  it('keeps the newest three events in the strip and opens the remaining history', async () => {
    const api = { ...fixtureApi(), listNotifications: vi.fn().mockResolvedValue([]), listAuditLog: vi.fn().mockResolvedValue(Array.from({ length: 5 }, (_, index) => ({ id: `event-${index}`, actor: 'admin', action: `Action ${index}`, resource_type: 'stack', resource_id: 'stack-1', ts: `2026-11-02T05:0${index}:00Z` }))) }
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    expect(wrapper.findAll('.lab-event-row')).toHaveLength(3)
    await wrapper.get('.event-history').trigger('click')
    expect(wrapper.get('.detail-panel').findAll('.lab-event-row')).toHaveLength(5)
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.findAll('.lab-event-row')).toHaveLength(3)
  })
  it('shows durable workload activity even when there are no alert notifications', async () => {
    const api = { ...fixtureApi(), listNotifications: vi.fn().mockResolvedValue([]), listAuditLog: vi.fn().mockResolvedValue([
      { id: 'audit-1', actor: 'admin', action: 'StartStack', resource_type: 'stack', resource_id: 'stack-1', ts: '2026-11-02T05:00:00Z', outcome: 'failed' },
    ]) }
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    expect(api.listAuditLog).toHaveBeenCalledOnce()
    const events = wrapper.get('[aria-label="Recent lab events"]')
    expect(events.text()).toContain('StartStack')
    expect(events.text()).toContain('plex · failed · admin')
    expect(events.get('.lab-event-row').classes()).toContain('error')
  })
  it('keeps notifications visible when audit history is unavailable', async () => {
    const api = { ...fixtureApi(), listAuditLog: vi.fn().mockRejectedValue(new ApiError('History offline', 503)) }
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('History offline')
    expect(wrapper.get('[aria-label="Recent lab events"]').text()).toContain('plex is running')
  })
  it('opens real workload tools and refreshes after commands complete', async () => {
    const api = interactiveFixtureApi()
    const list = vi.spyOn(api, 'listStacks')
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    await wrapper.get('[aria-label="Tools for plex"]').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(WorkloadDetail).props('stack').id).toBe('stack-1')
    wrapper.getComponent(CommandConsole).vm.$emit('completed')
    await flushPromises()
    expect(list).toHaveBeenCalledTimes(2)
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.findComponent(WorkloadDetail).exists()).toBe(false)
  })
  it('opens and cancels creation, then passes creation options to the shared editor', async () => {
    const wrapper = mount(LiveDashboard, { props: { api: interactiveFixtureApi() } })
    await flushPromises()
    const add = () => wrapper.findAll('button').find(button => button.text() === 'App')!
    await add().trigger('click')
    wrapper.getComponent(CreateApp).vm.$emit('close')
    await flushPromises()
    expect(wrapper.findComponent(CreateApp).exists()).toBe(false)
    await add().trigger('click')
    wrapper.getComponent(CreateApp).vm.$emit('create', ['create', 'web', '--primary', 'node-1'])
    await flushPromises()
    expect(document.querySelector('#palette-compose-title')?.textContent).toContain('Edit Compose')
    wrapper.unmount()
  })
  it('shows appliance updates for a selected host when the API supports them', async () => {
    const api = { ...fixtureApi(), listUpdateReleases: vi.fn().mockResolvedValue([]), getUpdateStatus: vi.fn().mockResolvedValue(null), runUpdateAction: vi.fn() }
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    await wrapper.get('[aria-label="Inspect lab-alpha"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[aria-label="Appliance updates"]').exists()).toBe(true)
    expect(api.getUpdateStatus).toHaveBeenCalledWith('node-1', 'management')
  })
  it.each([false, true])('shows the development indicator only in dev mode (%s)', async devMode => {
    const api = fixtureApi()
    const health = await api.getRaftHealth()
    api.getRaftHealth = vi.fn().mockResolvedValue({ ...health, dev_mode: devMode })
    const wrapper = mount(LiveDashboard, { props: { api } })
    await flushPromises()
    expect(wrapper.text().includes('Development mode')).toBe(devMode)
  })
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
