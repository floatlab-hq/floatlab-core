import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import LoginForm from './LoginForm.vue'
describe('Login form', () => {
  it('emits credentials and clears the password', async () => {
    const wrapper = mount(LoginForm)
    await wrapper.get('#username').setValue('admin')
    await wrapper.get('#password').setValue('secret')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('submit')).toEqual([[{ username: 'admin', password: 'secret' }]])
    expect((wrapper.get('#password').element as HTMLInputElement).value).toBe('')
  })
  it('disables controls while signing in and displays server errors', () => {
    const wrapper = mount(LoginForm, { props: { busy: true, error: 'Invalid credentials' } })
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[role="alert"]').text()).toBe('Invalid credentials')
  })
})
