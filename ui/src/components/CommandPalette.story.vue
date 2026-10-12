<script setup lang="ts">
import { ref } from 'vue'
import { commands } from '../data'
import CommandPalette from './CommandPalette.vue'
import UiButton from './UiButton.vue'

const open = ref(true)
const selected = ref('none')
</script>

<template>
  <Story title="2. Pattern/Command Palette">
    <UiButton variant="primary" @click="open = true">Open command palette</UiButton>
    <p>Selected: {{ selected }}</p>
    <CommandPalette :open="open" :commands="commands" @close="open = false" @select="selected = $event" />
  </Story>
</template>
