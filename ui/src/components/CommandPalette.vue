<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { Command } from '../types'
import UiButton from './UiButton.vue'
import UiInput from './UiInput.vue'

const props = defineProps<{
  open: boolean
  commands: Command[]
}>()

const emit = defineEmits<{
  close: []
  select: [commandId: string]
}>()

const dialog = ref<HTMLDialogElement>()
const query = ref('')
const filteredCommands = computed(() => {
  const search = query.value.toLowerCase()
  return props.commands.filter((command) => command.label.toLowerCase().includes(search))
})

watch(
  () => props.open,
  (open, wasOpen) => {
    if (!dialog.value) return

    if (open && !dialog.value.open) {
      if (typeof dialog.value.showModal === 'function') dialog.value.showModal()
      else dialog.value.open = true
    } else if (!open && wasOpen) {
      if (typeof dialog.value.close === 'function') dialog.value.close()
      else dialog.value.open = false
    }
  },
  { immediate: true, flush: 'post' },
)

function select(commandId: string) {
  emit('select', commandId)
  emit('close')
}
</script>

<template>
  <dialog ref="dialog" class="command-palette" @cancel="emit('close')">
    <label for="command-search">Search commands</label>
    <UiInput id="command-search" v-model="query" type="search" autofocus />

    <menu>
      <UiButton
        v-for="command in filteredCommands"
        :key="command.id"
        class="command-item"
        variant="ghost"
        @click="select(command.id)"
      >
        <span>{{ command.label }}</span>
        <span>{{ command.shortcut }}</span>
      </UiButton>
    </menu>

    <UiButton variant="ghost" @click="emit('close')">Esc — close</UiButton>
  </dialog>
</template>
