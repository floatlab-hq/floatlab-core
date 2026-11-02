import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { node } from '../../dashboard/live-fixtures'
import CreateApp from './CreateApp.vue'
describe('Create app', () => {
  it('collects CLI creation options and excludes the primary from standby choices', async () => {
    const wrapper = mount(CreateApp, { props: { nodes: [node, { ...node, id: 'node-2', name: 'beta' }] } })
    await wrapper.get('#create-app-name').setValue(' web ')
    expect(wrapper.get('#create-app-secondary').findAll('option').map(option => option.attributes('value'))).toEqual(['', 'node-2'])
    await wrapper.get('#create-app-secondary').setValue('node-2')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('create')).toEqual([[['create', 'web', '--primary', 'node-1', '--secondary', 'node-2', '--start']]])
    await wrapper.get('#create-app-primary').setValue('node-2')
    await wrapper.get('[role="switch"]').trigger('click')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('create')?.[1]).toEqual([['create', 'web', '--primary', 'node-2']])
    wrapper.unmount()
  })
  it('focuses the name field, traps focus, and restores it when closed', async () => {
    const button = document.createElement('button'); document.body.append(button); button.focus()
    const wrapper = mount(CreateApp, { props: { nodes: [node] }, attachTo: document.body })
    await flushPromises()
    expect(document.activeElement?.id).toBe('create-app-name')
    await wrapper.get('form').trigger('keydown', { key: 'Tab', shiftKey: true })
    expect(document.activeElement?.textContent).toBe('Continue to Compose')
    await wrapper.get('form').trigger('keydown', { key: 'Escape' })
    await wrapper.get('.palette-backdrop').trigger('mousedown')
    await wrapper.findAll('button').find(button => button.text() === 'Cancel')!.trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(3)
    wrapper.unmount()
    expect(document.activeElement).toBe(button)
    button.remove()
  })
  it('prevents creating without nodes', () => {
    const wrapper = mount(CreateApp, { props: { nodes: [] } })
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('Register a node')
    wrapper.unmount()
  })
})
