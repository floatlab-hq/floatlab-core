<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Node } from '@floatlab/client'
import UiInput from '../UiInput.vue'
import UiSelect from '../UiSelect.vue'
import UiSwitch from '../UiSwitch.vue'
import UiButton from '../UiButton.vue'
const props = defineProps<{ nodes: Node[] }>()
const emit = defineEmits<{ close: []; create: [args: string[]] }>()
const name = ref('')
const primary = ref(props.nodes[0]?.id ?? '')
const secondary = ref('')
const start = ref(true)
const form = ref<HTMLElement>()
const options = computed(() => props.nodes.map(node => ({ value: node.id, label: node.name })))
const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
onMounted(() => { void nextTick(() => form.value?.querySelector('input')?.focus()) })
onBeforeUnmount(() => previousFocus?.focus())
function submit() {
  emit('create', ['create', name.value.trim(), '--primary', primary.value, ...(secondary.value ? ['--secondary', secondary.value] : []), ...(start.value ? ['--start'] : [])])
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); emit('close') }
  if (event.key !== 'Tab') return
  const elements = [...form.value!.querySelectorAll<HTMLElement>('input, select, button:not(:disabled)')]
  const index = elements.indexOf(document.activeElement as HTMLElement)
  event.preventDefault()
  elements[(index + (event.shiftKey ? elements.length - 1 : 1)) % elements.length]?.focus()
}
</script>
<template>
  <div class="palette-backdrop" @mousedown.self="emit('close')">
    <form ref="form" class="palette-modal create-app" role="dialog" aria-modal="true" aria-labelledby="create-app-title" @submit.prevent="submit" @keydown="keydown">
      <h2 id="create-app-title">Add app</h2>
      <label for="create-app-name">App name</label><UiInput id="create-app-name" v-model="name" required pattern=".*\S.*" autocomplete="off" />
      <label for="create-app-primary">Primary node</label><UiSelect id="create-app-primary" v-model="primary" :options="options" required @change="secondary = ''" />
      <label for="create-app-secondary">Standby node (optional)</label><UiSelect id="create-app-secondary" v-model="secondary" :options="options.filter(option => option.value !== primary)" />
      <label>Start after creation<UiSwitch v-model="start" label="Start after creation" /></label>
      <p>The next step opens the Compose editor. Configure services there; the CLI validates the configuration before creating the app.</p>
      <p v-if="!nodes.length" role="status">Register a node before adding an app.</p>
      <div class="palette-actions"><UiButton type="button" @click="emit('close')">Cancel</UiButton><UiButton type="submit" :disabled="!nodes.length">Continue to Compose</UiButton></div>
    </form>
  </div>
</template>
<style scoped>
.create-app { display: grid; gap: 12px; }
label { display: flex; justify-content: space-between; align-items: center; }
p { font-size: var(--text-status); line-height: var(--line-body); }
</style>
