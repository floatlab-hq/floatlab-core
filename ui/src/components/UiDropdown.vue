<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import UiButton from './UiButton.vue'

defineProps<{ label: string; heading?: string; summary?: string; items: { id: string; label: string; disabled?: boolean }[] }>()
const emit = defineEmits<{ select: [id: string] }>()
const open = ref(false)
const trigger = ref<HTMLElement>()
const menu = ref<HTMLElement>()
const position = ref({ left: '0px', top: '0px', maxHeight: '300px' })
const id = useId()
function close(focus = false) {
  open.value = false
  if (focus) trigger.value?.querySelector('button')?.focus()
}
async function toggle() {
  if (open.value) { close(); return }
  const rect = trigger.value!.getBoundingClientRect()
  const below = window.innerHeight - rect.bottom - 12
  const above = rect.top - 12
  position.value = { left: `${Math.max(8, Math.min(rect.left, window.innerWidth - 248))}px`, top: `${below >= 160 || below >= above ? rect.bottom + 4 : Math.max(8, rect.top - Math.min(300, above))}px`, maxHeight: `${Math.max(80, Math.min(300, below >= 160 || below >= above ? below : above))}px` }
  open.value = true
  await nextTick()
  menu.value?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus()
}
function outside(event: globalThis.PointerEvent) {
  const target = event.target as globalThis.Node
  if (!trigger.value?.contains(target) && !menu.value?.contains(target)) close()
}
function keyboard(event: KeyboardEvent) {
  const buttons = [...menu.value!.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
  if (event.key === 'Escape') { event.preventDefault(); close(true) }
  else if (event.key === 'Tab') close(true)
  else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) && buttons.length) {
    event.preventDefault()
    const index = buttons.indexOf(document.activeElement as HTMLButtonElement)
    buttons[event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length]?.focus()
  }
}
function select(value: string) { close(true); emit('select', value) }
onMounted(() => { document.addEventListener('pointerdown', outside); window.addEventListener('resize', closeOnResize) })
function closeOnResize() { close() }
onBeforeUnmount(() => { document.removeEventListener('pointerdown', outside); window.removeEventListener('resize', closeOnResize) })
</script>

<template>
  <span ref="trigger" class="dropdown-trigger" @click.stop @keydown.esc.stop="close(true)">
    <slot name="trigger" :toggle="toggle" :open="open" :controls="id" />
  </span>
  <Teleport to="body">
    <div v-if="open" :id="id" ref="menu" class="ui-dropdown" :style="position" role="menu" :aria-label="label" @click.stop @keydown="keyboard">
      <header v-if="heading || summary"><strong>{{ heading }}</strong><span>{{ summary }}</span></header>
      <UiButton v-for="item in items" :key="item.id" variant="ghost" role="menuitem" :disabled="item.disabled" @click="select(item.id)">{{ item.label }}</UiButton>
    </div>
  </Teleport>
</template>

<style scoped>
.dropdown-trigger { display: inline-flex; }
.ui-dropdown { position: fixed; z-index: 6; display: grid; width: 240px; padding: 4px; overflow-y: auto; border: 1px solid var(--line); border-radius: var(--radius); background: var(--surface-recessed); color: var(--text-secondary); font-family: var(--font-mono); }
.ui-dropdown header { display: flex; justify-content: space-between; gap: 12px; padding: 8px 12px; border-bottom: 1px solid var(--line); font-size: 10px; }
.ui-dropdown header span { color: var(--text-placeholder); }
.ui-dropdown .button { text-align: left; white-space: normal; }
.ui-dropdown .button:disabled { opacity: .6; cursor: default; }
</style>
