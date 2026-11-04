import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { apps } from '../../dashboard/fixtures'
import AppDirectory from './AppDirectory.vue'
import AppPlateActions from './AppPlateActions.vue'

enableAutoUnmount(afterEach)
describe('App directory', () => {
  it('uses the six sortable columns and shared action plates', () => {
    const wrapper = mount(AppDirectory, { props: { apps: [{ ...apps[0]!, ports: ['5432'] }] } })
    expect(wrapper.findAll('[role="columnheader"]').map(cell => cell.text())).toEqual(['App', 'IP address', 'Ports', 'Runtime', 'Replication', 'Backup'])
    expect(wrapper.findAllComponents(AppPlateActions)).toHaveLength(3)
    expect(wrapper.get('.app-address').text()).toBe('172.16.23.13')
    expect(wrapper.get('.port-chip').text()).toBe('5432')
    expect(wrapper.find('.app-tools').exists()).toBe(false)
  })
  it('shows a direction arrow only on the sorted header', async () => {
    const wrapper = mount(AppDirectory, { props: { apps } })
    expect(wrapper.findAll('.column-sort svg')).toHaveLength(1)
    expect(wrapper.get('[aria-label="Sort by App"] svg').classes()).toContain('tabler-icon-arrow-up')
    await wrapper.get('[aria-label="Sort by IP address"]').trigger('click')
    expect(wrapper.find('[aria-label="Sort by App"] svg').exists()).toBe(false)
    expect(wrapper.findAll('.column-sort svg')).toHaveLength(1)
    expect(wrapper.get('[aria-label="Sort by IP address"] svg').classes()).toContain('tabler-icon-arrow-up')
    await wrapper.get('[aria-label="Sort by IP address"]').trigger('click')
    expect(wrapper.get('[aria-label="Sort by IP address"] svg').classes()).toContain('tabler-icon-arrow-down')
  })
  it('opens details from the row, name, and keyboard without activating nested controls', async () => {
    const wrapper = mount(AppDirectory, { props: { apps: [apps[0]!] }, global: { stubs: { Teleport: true } } })
    const row = wrapper.get('.app-row')
    await row.get('.app-identity').trigger('click')
    await row.trigger('keydown', { key: 'Enter' })
    await row.trigger('keydown', { key: ' ' })
    await row.get('.app-open').trigger('click')
    expect(wrapper.emitted('open')).toEqual(Array.from({ length: 4 }, () => ['postgres']))
    expect(row.attributes('aria-expanded')).toBe('true')
    await row.get('[data-plate-kind="runtime"] button').trigger('click')
    await wrapper.findAll('[role="menuitem"]').find(button => button.text() === 'Stop')!.trigger('click')
    expect(wrapper.emitted('command')).toEqual([['postgres', 'stop']])
    expect(wrapper.emitted('open')).toHaveLength(4)
  })
  it('keeps read-only rows and plates inactive', async () => {
    const wrapper = mount(AppDirectory, { props: { apps, readonly: true } })
    expect(wrapper.get('.app-row').attributes('tabindex')).toBeUndefined()
    expect(wrapper.find('.app-open').exists()).toBe(false)
    expect(wrapper.findAll('.plate-card').every(plate => plate.attributes('disabled') !== undefined)).toBe(true)
    await wrapper.get('.app-row').trigger('click')
    await wrapper.get('.app-row').trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('open')).toBeUndefined()
  })
  it('handles empty live data and filtered results', () => {
    const missing = mount(AppDirectory, { props: { apps: [{ ...apps[0]!, address: '', ports: [] }] } })
    expect(missing.get('.app-address').text()).toBe('—')
    expect(missing.get('.app-ports').text()).toBe('—')
    expect(mount(AppDirectory, { props: { apps: [], managed: true } }).text()).toContain('No apps')
    expect(mount(AppDirectory, { props: { apps: [] } }).text()).toContain('No apps match these filters.')
  })
  it('sorts each column in both directions without mutating data or opening a row', async () => {
    const data = [
      { ...apps[0]!, id: 'z', name: 'Zulu', state: 'Running' as const, replication: { label: 'Synced' }, backups: { label: 'Scheduled' } },
      { ...apps[0]!, id: 'a', name: 'Alpha', state: 'Failed' as const, replication: { label: 'Failed' }, backups: { label: 'Failed' } },
    ]
    const wrapper = mount(AppDirectory, { props: { apps: data } })
    const names = () => wrapper.findAll('.app-open').map(button => button.text())
    expect(names()).toEqual(['Alpha', 'Zulu'])
    for (const column of ['App', 'Runtime', 'Replication', 'Backup']) {
      const button = wrapper.get(`[aria-label="Sort by ${column}"]`)
      if (column !== 'App') await button.trigger('click')
      expect(names()).toEqual(['Alpha', 'Zulu'])
      expect(button.element.parentElement?.getAttribute('aria-sort')).toBe('ascending')
      await button.trigger('click')
      expect(names()).toEqual(['Zulu', 'Alpha'])
      expect(button.element.parentElement?.getAttribute('aria-sort')).toBe('descending')
    }
    expect(data.map(app => app.name)).toEqual(['Zulu', 'Alpha'])
    expect(wrapper.emitted('open')).toBeUndefined()
    await wrapper.setProps({ apps: [data[1]!] })
    expect(names()).toEqual(['Alpha'])
  })
  it('sorts IP addresses and ports naturally and opens details from their cells', async () => {
    const wrapper = mount(AppDirectory, { props: { apps: [
      { ...apps[0]!, id: 'ten', name: 'Ten', address: '10.0.5.10', ports: ['443', '8080'] },
      { ...apps[0]!, id: 'two', name: 'Two', address: '10.0.5.2', ports: ['80'] },
    ] } })
    for (const column of ['IP address', 'Ports']) {
      await wrapper.get(`[aria-label="Sort by ${column}"]`).trigger('click')
      expect(wrapper.findAll('.app-open').map(button => button.text())).toEqual(['Two', 'Ten'])
      await wrapper.get(`[aria-label="Sort by ${column}"]`).trigger('click')
      expect(wrapper.findAll('.app-open').map(button => button.text())).toEqual(['Ten', 'Two'])
    }
    await wrapper.get('.app-address').trigger('click')
    await wrapper.get('.port-chip').trigger('click')
    expect(wrapper.emitted('open')).toEqual([['ten']])
    expect(wrapper.get('.app-row').attributes('aria-expanded')).toBe('false')
    expect(wrapper.findAll('.port-chip').map(chip => chip.text())).toEqual(['443', '8080', '80'])
  })
  it('normalizes runtime states, restart transitions, and missing protection for sorting', async () => {
    const data = [
      { ...apps[0]!, id: 'running', name: 'Running', state: 'RunningPrimary' as const },
      { ...apps[0]!, id: 'idle', name: 'Idle', state: 'Idle' as const, standby: '', backup: '' },
      { ...apps[0]!, id: 'restart', name: 'Restart', runtimeMotion: 'restart' as const },
      { ...apps[0]!, id: 'standby', name: 'Standby', state: 'RunningBackup' as const },
    ]
    const wrapper = mount(AppDirectory, { props: { apps: data } })
    const names = () => wrapper.findAll('.app-open').map(button => button.text())
    await wrapper.get('[aria-label="Sort by Runtime"]').trigger('click')
    expect(names()).toEqual(['Restart', 'Idle', 'Running', 'Standby'])
    expect(wrapper.get('.standby-note').text()).toBe('Serving from standby')
    for (const column of ['Replication', 'Backup']) {
      await wrapper.get(`[aria-label="Sort by ${column}"]`).trigger('click')
      expect(names().at(-1)).toBe('Idle')
    }
  })
})

describe('App keyboard selection and containers', () => {
  it('selects in sorted order, expands real services, and leaves nested menus alone', async () => {
    const loadContainers = vi.fn(async () => [{ id: 'container', node_id: 'node', stack_id: 'stack', name: 'web-1', service: 'web', image: 'nginx:alpine', status: 'running' as const }])
    const wrapper = mount(AppDirectory, { props: { apps: apps.slice(0, 3), loadContainers }, attachTo: document.body })
    const rows = () => wrapper.findAll('.app-row')
    await rows()[0]!.trigger('click')
    await flushPromises()
    expect(loadContainers).toHaveBeenCalledWith(rows()[0]!.attributes('data-app-id'))
    expect(wrapper.get('.service-row').text()).toContain('webnginx:alpinerunning')
    await rows()[0]!.trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(document.activeElement).toBe(rows()[1]!.element)
    expect(rows()[1]!.attributes('aria-expanded')).toBe('true')
    await rows()[1]!.trigger('keydown', { key: 'ArrowUp' })
    expect(document.activeElement).toBe(rows()[0]!.element)
    await rows()[0]!.trigger('keydown', { key: 'ArrowLeft' })
    expect(wrapper.find('.app-services').exists()).toBe(false)
    const beforeExpand = wrapper.emitted('open')!.length
    await rows()[0]!.trigger('keydown', { key: 'ArrowRight' })
    await flushPromises()
    expect(wrapper.find('.app-services').exists()).toBe(true)
    expect(wrapper.emitted('open')).toHaveLength(beforeExpand + 1)
    await rows()[0]!.get('[data-plate-kind="runtime"] button').trigger('keydown', { key: 'ArrowDown' })
    expect(rows()[0]!.classes()).toContain('selected')
    await rows()[0]!.trigger('keydown', { key: 'End' })
    expect(document.activeElement).toBe(rows()[2]!.element)
    await rows()[2]!.trigger('keydown', { key: 'ArrowDown' })
    expect(document.activeElement).toBe(rows()[2]!.element)
    await rows()[2]!.trigger('keydown', { key: 'Home' })
    await flushPromises()
    expect(document.activeElement).toBe(rows()[0]!.element)
    await wrapper.get('.service-row').trigger('click')
    expect(wrapper.emitted('open')?.at(-1)).toEqual([rows()[0]!.attributes('data-app-id')])
  })
  it('handles empty results, loading, errors, and late responses from another selection', async () => {
    let resolve!: (value: []) => void
    const wrapper = mount(AppDirectory, { props: { apps: apps.slice(0, 2), loadContainers: vi.fn(() => new Promise<[]>(done => { resolve = done })) } })
    await wrapper.get('.app-row').trigger('click')
    expect(wrapper.get('[role="status"]').text()).toBe('Loading containers…')
    const finishFirst = resolve
    await wrapper.findAll('.app-row')[1]!.trigger('click')
    resolve([])
    await flushPromises()
    expect(wrapper.text()).toContain('No containers')
    finishFirst([])
    await flushPromises()
    await wrapper.setProps({ loadContainers: async () => { throw new Error('Host offline') }, apps: [...apps.slice(0, 2)] })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('Host offline')
    await wrapper.setProps({ apps: [] })
    expect(wrapper.find('.app-services').exists()).toBe(false)
  })
})
