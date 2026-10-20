import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { UpdateApi } from '@floatlab/client'
import ApplianceUpdates from './ApplianceUpdates.vue'
enableAutoUnmount(afterEach)
afterEach(() => vi.useRealTimers())
function fixture(): UpdateApi {
  return {
    listUpdateReleases: vi.fn().mockResolvedValue([{ tag_name: 'manage-v1.2.3', prerelease: true }]),
    getUpdateStatus: vi.fn().mockResolvedValue(null),
    runUpdateAction: vi.fn().mockResolvedValue({ operation_id: 'update', state: 'running', phase: 'preflight', target_release: 'manage-v1.2.3' }),
  }
}
describe('Appliance updates', () => {
  it('selects a release and explicitly submits a management update', async () => {
    const api = fixture()
    const wrapper = mount(ApplianceUpdates, { props: { api, nodeId: 'local' } })
    await flushPromises()
    expect(wrapper.text()).toContain('No update transaction')
    expect(wrapper.text()).toContain('manage-v1.2.3 (prerelease)')
    await wrapper.findAll('select')[1]!.setValue('manage-v1.2.3')
    await wrapper.findAll('button').find(button => button.text() === 'Start management update')!.trigger('click')
    expect(api.runUpdateAction).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(button => button.text() === 'Confirm start')!.trigger('click')
    await flushPromises()
    expect(api.runUpdateAction).toHaveBeenCalledWith('local', 'management', 'start', { release: 'manage-v1.2.3' }, expect.any(String))
    expect(wrapper.text()).toContain('running · preflight')
  })
  it('shows host reboot consequences and allows cancelling before submission', async () => {
    const api = fixture()
    vi.mocked(api.getUpdateStatus).mockResolvedValue({ operation_id: 'trial', state: 'ready', phase: 'staged', error: 'Earlier warning', rollback_error: 'Dataset busy' })
    const wrapper = mount(ApplianceUpdates, { props: { api, nodeId: 'local' } })
    await flushPromises()
    await wrapper.findAll('select')[0]!.setValue('host')
    await flushPromises()
    expect(api.listUpdateReleases).toHaveBeenLastCalledWith('local', 'host')
    expect(wrapper.text()).toContain('a kernel hang may require a physical reset')
    await wrapper.findAll('button').find(button => button.text() === 'Reboot into trial')!.trigger('click')
    expect(wrapper.text()).toContain('interrupts its workloads')
    await wrapper.findAll('button').find(button => button.text() === 'Cancel')!.trigger('click')
    expect(wrapper.find('[aria-label="Confirm update action"]').exists()).toBe(false)
    expect(api.runUpdateAction).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Recovery failed: Dataset busy')
  })
  it('keeps an idempotency key across an uncertain request and reports rollback consequences', async () => {
    const api = fixture()
    vi.mocked(api.getUpdateStatus).mockResolvedValue({ operation_id: 'trial', state: 'failed', phase: 'checking' })
    vi.mocked(api.runUpdateAction).mockRejectedValueOnce(new Error('Connection interrupted'))
    const wrapper = mount(ApplianceUpdates, { props: { api, nodeId: 'local' } })
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'Roll back')!.trigger('click')
    expect(wrapper.text()).toContain('Changes to workloads are not undone')
    await wrapper.findAll('button').find(button => button.text() === 'Confirm rollback')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Hostd continues accepted updates')
    await wrapper.findAll('button').find(button => button.text() === 'Confirm rollback')!.trigger('click')
    await flushPromises()
    const calls = vi.mocked(api.runUpdateAction).mock.calls
    expect(calls).toHaveLength(2)
    expect(calls[0]![4]).toBe(calls[1]![4])
    expect(calls[1]![3]).toEqual({ operation_id: 'trial' })
  })
  it('reconnects after maintenance and stops polling on unmount', async () => {
    vi.useFakeTimers()
    const api = fixture()
    vi.mocked(api.getUpdateStatus).mockRejectedValueOnce(new Error('Management restarting'))
    const wrapper = mount(ApplianceUpdates, { props: { api, nodeId: 'local' } })
    await flushPromises()
    expect(wrapper.text()).toContain('Management restarting')
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.text()).not.toContain('Management restarting')
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.getUpdateStatus).toHaveBeenCalledTimes(2)
  })
  it('ignores pending responses after the component is unmounted', async () => {
    let complete: (value: null) => void = () => undefined
    const api = fixture()
    vi.mocked(api.getUpdateStatus).mockImplementation(() => new Promise(resolve => { complete = resolve }))
    const wrapper = mount(ApplianceUpdates, { props: { api, nodeId: 'local' } })
    wrapper.unmount()
    complete(null)
    await flushPromises()
    expect(api.listUpdateReleases).not.toHaveBeenCalled()
  })
})
