import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ApiForm from './ApiForm.vue'
import { coreForms } from '../forms'
import type { FormSchema } from '../forms/schema'
describe('API form', () => {
  it('uses contract fields, required flags, options and defaults', async () => {
    const wrapper = mount(ApiForm, { props: { schema: coreForms.NodeCreate, modelValue: { name: 'node-01', hostname: 'node-01.local', addresses: '[{"type":"LAN-6","address":"2001:db8::1/64","interface":"eth0"}]', zfs_pool: 'floatlab' } } })
    expect(wrapper.findAll('input[required]')).toHaveLength(3)
    expect(wrapper.get('select').element.value).toBe('secondary')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('submit')![0]![0]).toMatchObject({ name: 'node-01', role: 'secondary', addresses: [{ type: 'LAN-6', address: '2001:db8::1/64', interface: 'eth0' }] })
    await wrapper.get('input').setValue('renamed')
    expect(wrapper.emitted('update:modelValue')![0]![0]).toMatchObject({ name: 'renamed' })
  })
  it('reports invalid structured input and recovers after correction', async () => {
    const wrapper = mount(ApiForm, { props: { schema: coreForms.NodeUpdate, modelValue: { addresses: 'bad' } } })
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role="alert"]').text()).toContain('valid JSON')
    expect(wrapper.emitted('submit')).toBeUndefined()
    await wrapper.setProps({ modelValue: { addresses: '[]' } })
    await wrapper.get('form').trigger('submit')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.emitted('submit')).toEqual([[{ addresses: [] }]])
  })
  it('emits enum, JSON, boolean and multiline edits without leaking extra keys', async () => {
    const schema: FormSchema = { name: 'Controls', fields: [{ key: 'on', label: 'On', type: 'boolean' }, { key: 'mode', label: 'Mode', type: 'enum', options: [{ value: 'manual', label: 'Manual' }] }, { key: 'data', label: 'Data', type: 'json' }, { key: 'compose_file', label: 'Compose', type: 'multiline' }] }
    const wrapper = mount(ApiForm, { props: { schema, modelValue: { on: false, compose_file: 'services:\n  db: {}', extra: 'ignored' } } })
    await wrapper.get('[role="switch"]').trigger('click')
    await wrapper.get('select').setValue('manual')
    await wrapper.findAll('textarea')[0]!.setValue('{}')
    await wrapper.findAll('textarea')[1]!.setValue('services: {}')
    expect(wrapper.emitted('update:modelValue')).toHaveLength(4)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('submit')).toEqual([[{ on: true, mode: 'manual', data: {}, compose_file: 'services: {}' }]])
  })
  it('keeps deprecated fields out of new app creation', () => {
    expect(coreForms.StackCreate.fields.map(field => field.key)).toEqual(['name', 'compose_file'])
    expect(coreForms.AlertRuleCreate.fields.find(field => field.key === 'enabled')?.default).toBe(true)
  })
})
