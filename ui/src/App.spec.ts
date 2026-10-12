import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'
import LoginForm from './components/LoginForm.vue'
import { fixtureApi } from './dashboard/live-fixtures'
vi.mock('./api/client', () => ({ browserApi: vi.fn() }))
import { browserApi } from './api/client'
enableAutoUnmount(afterEach)
afterEach(() => { vi.useRealTimers(); vi.clearAllMocks() })
describe('App authentication', () => {
  it('signs in, sends bearer credentials, and signs out', async () => {
    const api = fixtureApi()
    const login = vi.spyOn(api, 'createAuthToken')
    vi.mocked(browserApi).mockReturnValue(api)
    const wrapper = mount(App)
    wrapper.getComponent(LoginForm).vm.$emit('submit', { username: 'admin', password: 'secret' })
    await flushPromises()
    expect(login).toHaveBeenCalledWith({ username: 'admin', password: 'secret' })
    expect(browserApi).toHaveBeenCalledWith('story-only')
    expect(wrapper.text()).toContain('lab-alpha')
    await wrapper.findAll('button').find(button => button.text() === 'Sign out')!.trigger('click')
    expect(wrapper.findComponent(LoginForm).exists()).toBe(true)
  })
  it('shows a login failure', async () => {
    const api = fixtureApi()
    api.createAuthToken = vi.fn().mockRejectedValue(new Error('offline'))
    vi.mocked(browserApi).mockReturnValue(api)
    const wrapper = mount(App)
    wrapper.getComponent(LoginForm).vm.$emit('submit', { username: 'admin', password: 'secret' })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('Could not sign in')
  })
  it('expires the in-memory session', async () => {
    vi.useFakeTimers()
    const api = fixtureApi()
    api.createAuthToken = vi.fn().mockResolvedValue({ access_token: 'token', token_type: 'Bearer', expires_in: 1 })
    vi.mocked(browserApi).mockReturnValue(api)
    const wrapper = mount(App)
    wrapper.getComponent(LoginForm).vm.$emit('submit', { username: 'admin', password: 'secret' })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1000)
    expect(wrapper.findComponent(LoginForm).exists()).toBe(true)
  })
})
