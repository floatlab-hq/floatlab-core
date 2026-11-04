import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import { apps } from '../../dashboard/fixtures'
import type { DirectoryApp } from '../../dashboard/types'
import AppPlateActions from './AppPlateActions.vue'
enableAutoUnmount(afterEach)
function render(app: Partial<DirectoryApp> = {}, kind: 'runtime' | 'replication' | 'backup' = 'runtime') {
  return mount(AppPlateActions, { props: { app: { ...apps[0]!, hostId: 'node-01', ...app }, kind }, global: { stubs: { Teleport: true } } })
}
describe('App plate actions', () => {
  it.each([['Running', 'Stop', 'stop'], ['Stopped', 'Start', 'start'], ['Failed', 'Start', 'start'], ['Running', 'Restart', 'restartStack']] as const)('offers %s lifecycle actions', async (state, label, action) => {
    const wrapper = render({ state })
    await wrapper.get('.plate-card').trigger('click')
    expect(wrapper.get('.ui-dropdown header').text()).toContain('Postgres')
    await wrapper.findAll('[role="menuitem"]').find(button => button.text() === label)!.trigger('click')
    expect(wrapper.emitted('action')).toEqual([[action]])
  })
  it('locks mutations for transitional and cached apps but leaves inspection available', async () => {
    const wrapper = render({ state: 'Starting', replica: true })
    await wrapper.get('.plate-card').trigger('click')
    for (const name of ['Start', 'Restart', 'Delete app…']) expect(wrapper.findAll('[role="menuitem"]').find(button => button.text() === name)!.attributes('disabled')).toBeDefined()
    await wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'View logs')!.trigger('click')
    expect(wrapper.emitted('action')).toEqual([['logs']])
  })
  it('routes configured standby switches and replication commands', async () => {
    const wrapper = render({ hostSwitch: 'triggerFailover' }, 'replication')
    await wrapper.get('.plate-card').trigger('click')
    await wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'Sync now')!.trigger('click')
    await wrapper.get('.plate-card').trigger('click')
    await wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'Fail over to standby')!.trigger('click')
    expect(wrapper.emitted('action')).toEqual([['sync'], ['triggerFailover']])
  })
  it('does not offer syncing without a standby, during transfer, or while standby is active', async () => {
    for (const app of [{ standby: '' }, { replication: { label: 'Syncing', busy: true } }, { hostSwitch: 'restoreStack' as const }, { protectionAvailable: false }]) {
      const wrapper = render(app, 'replication')
      await wrapper.get('.plate-card').trigger('click')
      expect(wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'Sync now')!.attributes('disabled')).toBeDefined()
      wrapper.unmount()
    }
  })
  it('opens backup configuration and enables backup runs only with a destination', async () => {
    const wrapper = render({ backups: { label: 'Scheduled', host: 'node-03' } }, 'backup')
    await wrapper.get('.plate-card').trigger('click')
    await wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'Back up now')!.trigger('click')
    await wrapper.get('.plate-card').trigger('click')
    await wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'Configure backups')!.trigger('click')
    expect(wrapper.emitted('action')).toEqual([['backup-run'], ['backups']])
    const empty = render({ backup: '' }, 'backup')
    expect(empty.get('.plate-card').text()).toBe('None')
    await empty.get('.plate-card').trigger('click')
    expect(empty.findAll('[role="menuitem"]').find(button => button.text() === 'Back up now')!.attributes('disabled')).toBeDefined()
  })
  it('shows the accepted restart transition while locking lifecycle commands', async () => {
    const wrapper = render({ runtimeMotion: 'restart' })
    expect(wrapper.get('.plate-card').text()).toContain('Restarting')
    await wrapper.get('.plate-card').trigger('click')
    expect(wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'Stop')!.attributes('disabled')).toBeDefined()
  })
  it('disables plate triggers while an inline action is pending', () => {
    const wrapper = mount(AppPlateActions, { props: { app: { ...apps[0]!, actionPending: true } } })
    expect(wrapper.findAll('.plate-card').every(plate => plate.attributes('disabled') !== undefined)).toBe(true)
  })
  it('renders all plates and prevents mutation in readonly contexts', () => {
    const wrapper = mount(AppPlateActions, { props: { app: apps[0]!, readonly: true } })
    expect(wrapper.findAll('.plate-card')).toHaveLength(3)
    expect(wrapper.findAll('.plate-card').every(plate => plate.attributes('disabled') !== undefined)).toBe(true)
  })
})
