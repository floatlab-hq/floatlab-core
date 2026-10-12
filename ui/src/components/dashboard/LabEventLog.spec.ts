import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { events } from '../../dashboard/fixtures'
import LabEventLog from './LabEventLog.vue'
import UiNodePlate from './UiNodePlate.vue'

describe('Lab event log', () => {
  it('hides disconnected inspection controls in read-only mode', () => {
    const wrapper = mount(LabEventLog, { props: { events, readonly: true } })
    expect(wrapper.findAll('button')).toHaveLength(0)
  })
  it('renders severity roles, reused node plates, and event details', async () => {
    const wrapper = mount(LabEventLog, { props: { events } })
    expect(wrapper.findAllComponents(UiNodePlate)).toHaveLength(3)
    expect(wrapper.findAll('.lab-event-row').map((row) => row.classes())).toEqual([
      ['lab-event-row', 'error'], ['lab-event-row', 'info'], ['lab-event-row', 'warning'],
    ])
    expect(wrapper.text()).toContain(events[0]!.message)
    expect(wrapper.findAll('button')).toHaveLength(1)
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('select')).toEqual([['backup']])
  })
  it('supports an empty feed', () => {
    expect(mount(LabEventLog, { props: { events: [] } }).findAll('.lab-event-row')).toHaveLength(0)
  })
})
