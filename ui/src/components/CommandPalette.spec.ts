import { mount } from '@vue/test-utils'
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import CommandPalette from './CommandPalette.vue'

const commands = [
  { id: 'immich', label: 'Open Immich', shortcut: '↵' },
  { id: 'restart', label: 'Restart Minecraft', shortcut: 'R' },
  { id: 'alerts', label: 'Review alerts', shortcut: 'A' },
]

const showModal = vi.fn(function (this: HTMLDialogElement) {
  this.open = true
})
const close = vi.fn(function (this: HTMLDialogElement) {
  this.open = false
})
const originalShowModal = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal')
const originalClose = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'close')

beforeAll(() => {
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: showModal })
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: close })
})

beforeEach(() => {
  showModal.mockClear()
  close.mockClear()
})

afterAll(() => {
  if (originalShowModal) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', originalShowModal)
  else Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal')
  if (originalClose) Object.defineProperty(HTMLDialogElement.prototype, 'close', originalClose)
  else Reflect.deleteProperty(HTMLDialogElement.prototype, 'close')
})

describe('CommandPalette', () => {
  it('syncs the open prop with the native dialog', async () => {
    const wrapper = mount(CommandPalette, { props: { open: false, commands } })

    await wrapper.setProps({ open: true })
    expect(showModal).toHaveBeenCalledOnce()

    await wrapper.setProps({ open: false })
    expect(close).toHaveBeenCalledOnce()
  })

  it('filters commands by label case-insensitively', async () => {
    const wrapper = mount(CommandPalette, { props: { open: false, commands } })

    await wrapper.get('input').setValue('RESTART')

    expect(wrapper.findAll('menu button').map((button) => button.text())).toEqual(['Restart MinecraftR'])
  })

  it('emits select then close when a command is chosen', async () => {
    const wrapper = mount(CommandPalette, { props: { open: false, commands } })

    await wrapper.findAll('menu button')[1].trigger('click')

    expect(wrapper.emitted()).toMatchObject({ select: [['restart']], close: [[]] })
  })

  it('emits close for Escape and the cancel button', async () => {
    const wrapper = mount(CommandPalette, { props: { open: false, commands } })

    await wrapper.get('dialog').trigger('cancel')
    await wrapper.get('.button.ghost').trigger('click')

    expect(wrapper.emitted('close')).toEqual([[], []])
  })
})
