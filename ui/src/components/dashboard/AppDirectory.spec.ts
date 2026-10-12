import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { apps } from '../../dashboard/fixtures'
import AppDirectory from './AppDirectory.vue'
import UiNodePlate from './UiNodePlate.vue'
import UiStatusBadge from './UiStatusBadge.vue'

describe('App directory', () => {
  it('hides unsupported controls in the live read-only directory', () => {
    const wrapper = mount(AppDirectory, { props: { apps: [{ ...apps[0]!, logo: '' }], readonly: true } })
    expect(wrapper.find('.asset-icon').exists()).toBe(false)
    expect(wrapper.find('[aria-label="Choose app columns"]').exists()).toBe(false)
    expect(wrapper.get('.status-badge').attributes('disabled')).toBeDefined()
    expect(wrapper.find('.app-tools').exists()).toBe(false)
    expect(wrapper.find('.app-open').exists()).toBe(false)
    expect(wrapper.find('.directory-link').exists()).toBe(false)
  })
  it('composes three node plates and a status for each app', () => {
    const wrapper = mount(AppDirectory, { props: { apps } })
    expect(wrapper.findAllComponents(UiNodePlate)).toHaveLength(apps.length * 3)
    expect(wrapper.findAllComponents(UiStatusBadge)).toHaveLength(apps.length)
    expect(wrapper.findAll('.app-row')).toHaveLength(6)
    expect(wrapper.text()).toContain('company-website')
    expect(wrapper.findAll('.asset-icon')).toHaveLength(6)
  })
  it('emits every app action with the correct identity', async () => {
    const wrapper = mount(AppDirectory, { props: { apps: [apps[0]!] } })
    await wrapper.get('[aria-label="Open Postgres"]').trigger('click')
    await wrapper.get('[aria-label="Running — change app state"]').trigger('click')
    await wrapper.get('.directory-link').trigger('click')
    await wrapper.get('[aria-label="Tools for Postgres"]').trigger('click')
    await wrapper.get('[aria-label="Choose app columns"]').trigger('click')
    for (const action of ['open', 'state', 'network', 'tools']) expect(wrapper.emitted(action)).toEqual([['postgres']])
    expect(wrapper.emitted('columns')).toHaveLength(1)
  })
  it('handles apps without TLS and an empty filtered result', () => {
    const wrapper = mount(AppDirectory, { props: { apps: [{ ...apps[0]!, tls: false }] } })
    expect(wrapper.get('.app-security').text()).toBe('—')
    expect(mount(AppDirectory, { props: { apps: [] } }).text()).toContain('No apps match these filters.')
  })
})
