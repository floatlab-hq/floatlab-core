<script setup lang="ts">
import { ref } from 'vue'
import UiInput from './UiInput.vue'

const value = ref('Search commands')
</script>

<template>
  <Story title="1. Primitive/UI Input">
    <UiInput v-model="value" placeholder="Search commands" />

    <UiInput v-model="value" variant="field" aria-label="Display name" />
    <template #controls>
      <label>Value <input v-model="value" /></label>
    </template>
  </Story>
</template>

<style scoped>
label { display: block; margin: 12px; }
</style>
