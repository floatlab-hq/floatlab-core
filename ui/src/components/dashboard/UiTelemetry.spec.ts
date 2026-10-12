import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiTelemetry from './UiTelemetry.vue'
import ResourceTrack from '../ResourceTrack.vue'

describe('Telemetry', () => {
  it('shows missing metrics without zero-valued tracks', () => {
    const wrapper = mount(UiTelemetry, { props: { cpu: '—' } })
    expect(wrapper.findAllComponents(ResourceTrack)).toHaveLength(0)
    expect(wrapper.find('[aria-label="Memory unavailable"]').exists()).toBe(true)
    expect(wrapper.find('[aria-label="Disk unavailable"]').exists()).toBe(true)
  })
  it('uses compact ResourceTrack primitives and exposes the readings', () => {
    const wrapper = mount(UiTelemetry, { props: { cpu: '⠴⠼⠦', memory: 38, disk: 40 } })
    expect(wrapper.findAllComponents(ResourceTrack).map((track) => track.props())).toEqual([
      expect.objectContaining({ percent: 38, compact: true }),
      expect.objectContaining({ percent: 40, compact: true }),
    ])
    expect(wrapper.find('[aria-label="Memory 38%"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('C ⠴⠼⠦')
  })
})
