<script setup lang="ts">
withDefaults(defineProps<{ variant?: 'default' | 'field' }>(), { variant: 'default' })
const model = defineModel<string>({ default: '' })
</script>

<template>
  <input v-model="model" class="ui-input" :class="{ field: variant === 'field' }" />
</template>

<style scoped>
.ui-input.field { height: 36px; border-color: transparent; background: var(--surface-default); color: var(--text-primary); font: inherit; font-size: var(--text-status); }
.ui-input.field:focus-visible { outline: 2px solid var(--omarchy-accent); outline-offset: 2px; }
</style>
