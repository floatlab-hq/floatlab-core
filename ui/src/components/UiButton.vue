<script setup lang="ts">
withDefaults(
  defineProps<{
    variant?: 'default' | 'plain' | 'primary' | 'outline' | 'ghost' | 'surface'
    size?: 'default' | 'small'
  }>(),
  { variant: 'default', size: 'default' },
)
</script>

<template>
  <button
    type="button"
    class="button"
    :class="[variant !== 'default' && variant, size !== 'default' && size]"
  >
    <slot></slot>
  </button>
</template>
