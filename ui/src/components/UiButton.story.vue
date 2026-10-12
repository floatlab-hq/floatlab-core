<script setup lang="ts">
import UiButton from './UiButton.vue'
</script>

<template>
  <Story title="1. Primitive/UI Button">
    <div class="button-story">
      <UiButton variant="surface">Surface</UiButton>
      <UiButton>Default</UiButton>
      <UiButton variant="plain">Plain</UiButton>
      <UiButton variant="primary">Primary</UiButton>
      <UiButton variant="outline">Outline</UiButton>
      <UiButton variant="ghost">Ghost</UiButton>
      <UiButton variant="outline" size="small">Small</UiButton>
      <UiButton disabled>Disabled</UiButton>
    </div>
  </Story>
</template>

<style scoped>
.button-story { display: flex; align-items: center; gap: 12px; }
</style>
