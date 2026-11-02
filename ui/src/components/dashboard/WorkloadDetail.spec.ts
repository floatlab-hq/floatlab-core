import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { interactiveFixtureApi, stack } from '../../dashboard/live-fixtures'
import WorkloadDetail from './WorkloadDetail.vue'
describe('Workload detail', () => {
  it('offers only implemented actions and routes stack and container commands by ID', async () => {
    const wrapper = mount(WorkloadDetail, { props: { stack, api: interactiveFixtureApi([stack]) } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('Restart')
    expect(wrapper.text()).not.toContain('Start after')
    const commands = [
      ['Stop', ['stop', stack.id]], ['View logs', ['logs', stack.id]],
      ['Metrics', ['stats', stack.id]], ['View Compose', ['config', stack.id]], ['Inspect', ['info', stack.id]],
      ['View snapshots', ['snapshot', 'list', stack.id]], ['Create snapshot', ['snapshot', 'create', stack.id]],
      ['Edit Compose', ['edit', stack.id]], ['Upgrade images', ['upgrade', stack.id]], ['Delete app', ['delete', stack.id]],
    ] as const
    for (const [label, args] of commands) {
      await wrapper.findAll('button').find(button => button.text() === label)!.trigger('click')
      expect(wrapper.emitted('command')?.at(-1)).toEqual([[...args]])
    }
    const container = wrapper.get('.container-detail')
    for (const [index, command] of ['logs', 'info', 'start', 'stop', 'exec'].entries()) {
      await container.findAll('button')[index]!.trigger('click')
      expect(wrapper.emitted('command')?.at(-1)).toEqual(command === 'exec' ? [[command, stack.id, 'container-1', '--'], false] : [[command, stack.id, 'container-1']])
    }
    await wrapper.setProps({ stack: { ...stack, state: 'Idle' } })
    await wrapper.findAll('button').find(button => button.text() === 'Start')!.trigger('click')
    expect(wrapper.emitted('command')?.at(-1)).toEqual([['start', stack.id]])
    await wrapper.get('[aria-label="Close detail panel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ stack: { ...stack, replica: true } })
    expect(wrapper.text()).not.toContain('Stop container')
    expect(wrapper.text()).not.toContain('Delete app')
    expect(wrapper.text()).toContain('Cached peer view')
    wrapper.unmount()
  })
  it('shows empty containers and API errors', async () => {
    const wrapper = mount(WorkloadDetail, { props: { stack, api: { ...interactiveFixtureApi(), listContainers: async () => [] } } })
    expect(wrapper.text()).toContain('Loading containers')
    await flushPromises()
    expect(wrapper.text()).toContain('No containers')
    wrapper.unmount()
    const failed = mount(WorkloadDetail, { props: { stack, api: { ...interactiveFixtureApi(), listContainers: vi.fn().mockRejectedValue(new Error('Offline')) } } })
    await flushPromises()
    expect(failed.get('[role="alert"]').text()).toBe('Offline')
    failed.unmount()
  })
  it('reloads containers when the dashboard refreshes the stack', async () => {
    const api = interactiveFixtureApi([stack])
    const list = vi.spyOn(api, 'listContainers')
    const wrapper = mount(WorkloadDetail, { props: { stack, api } })
    await flushPromises()
    await api.stopContainer(stack.id)
    await wrapper.setProps({ stack: { ...stack } })
    await flushPromises()
    expect(list).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('nginx:alpine · stopped')
    wrapper.unmount()
  })
})
