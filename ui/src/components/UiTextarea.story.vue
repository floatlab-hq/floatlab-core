<script setup lang="ts">
import { ref } from 'vue'
import UiTextarea from './UiTextarea.vue'
const value = ref('services:\n  postgres:\n    image: postgres:17')
</script>
<template><Story title="1. Primitive/Textarea"><UiTextarea v-model="value" aria-label="Compose file" rows="6" /><UiTextarea disabled aria-label="Unavailable" /></Story></template>
