import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { hosts } from '../../dashboard/fixtures'
import HostDirectory from './HostDirectory.vue'
import UiNodePlate from './UiNodePlate.vue'
import UiTelemetry from './UiTelemetry.vue'

describe('Host directory', () => {
  it('composes identity, health, and telemetry across all host states', () => {
    const wrapper = mount(HostDirectory, { props: { hosts } })
    expect(wrapper.findAllComponents(UiNodePlate)).toHaveLength(3)
    expect(wrapper.findAllComponents(UiTelemetry)).toHaveLength(3)
    expect(wrapper.findAll('.host-row')).toHaveLength(3)
    expect(wrapper.find('[aria-label="Backup attention"]').exists()).toBe(true)
    expect(wrapper.findAll('.host-app-count')).toHaveLength(2)
    expect(wrapper.findAll('.directory-link')).toHaveLength(4)
    expect(wrapper.text()).toContain('7 standby')
  })
  it('emits host-specific actions', async () => {
    const wrapper = mount(HostDirectory, { props: { hosts } })
    await wrapper.get('[aria-label="Tools for node-01"]').trigger('click')
    await wrapper.get('[aria-label="Inspect node-02"]').trigger('click')
    await wrapper.findAll('.directory-link')[0]!.trigger('click')
    await wrapper.findAll('.directory-link')[1]!.trigger('click')
    expect(wrapper.emitted('tools')).toEqual([['node-01']])
    expect(wrapper.emitted('inspect')).toEqual([['node-02']])
    expect(wrapper.emitted('connect')).toEqual([['node-01', 'network'], ['node-01', 'host']])
  })
})
