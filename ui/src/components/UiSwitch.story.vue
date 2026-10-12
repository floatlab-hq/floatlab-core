<script setup lang="ts">
import UiSwitch from './UiSwitch.vue'
import { ref } from 'vue'
const value = ref(true)
</script>
<template><Story title="1. Primitive/Switch"><UiSwitch v-model="value" label="Resource alerts" /><UiSwitch label="Unavailable" disabled /></Story></template>
