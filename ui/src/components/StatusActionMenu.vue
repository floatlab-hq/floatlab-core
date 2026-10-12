<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Operation, Status } from '../types'
import UiButton from './UiButton.vue'

const props = defineProps<{
  status: Status
  pendingOperation?: Operation
}>()

const emit = defineEmits<{
  operation: [operation: Operation]
}>()

const open = ref(false)
const statusClass = computed(() =>
  props.status === 'Failed' ? 'error' : props.status.toLowerCase(),
)

function selectOperation(operation: Operation) {
  emit('operation', operation)
  open.value = false
}
</script>

<template>
  <div class="status-action" @keydown.esc="open = false">
    <UiButton
      class="status"
      :class="statusClass"
      type="button"
      aria-haspopup="menu"
      :aria-expanded="open"
      @click="open = !open"
    >
      {{ status }} <b aria-hidden="true">⌄</b>
    </UiButton>

    <div v-if="open" class="status-menu" role="menu">
      <UiButton
        type="button"
        role="menuitem"
        :disabled="pendingOperation === 'start'"
        @click="selectOperation('start')"
      >
        Start
      </UiButton>
      <UiButton
        type="button"
        role="menuitem"
        :disabled="pendingOperation === 'restart'"
        @click="selectOperation('restart')"
      >
        Restart
      </UiButton>
      <UiButton
        type="button"
        role="menuitem"
        :disabled="pendingOperation === 'stop'"
        @click="selectOperation('stop')"
      >
        Stop
      </UiButton>
    </div>
  </div>
</template>
