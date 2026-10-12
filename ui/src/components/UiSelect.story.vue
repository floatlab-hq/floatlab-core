<script setup lang="ts">
import UiSelect from './UiSelect.vue'
import { ref } from 'vue'
const value = ref('floatlab')
const many = ref(['CPU', 'Memory'])
const options = ['floatlab', 'archive'].map(value => ({ value, label: value }))
const categories = ['CPU', 'Memory', 'Storage'].map(value => ({ value, label: value }))
</script>
<template><Story title="1. Primitive/Select"><UiSelect v-model="value" :options="options" aria-label="Storage pool" /><UiSelect v-model="many" :options="categories" multiple aria-label="Alert categories" /></Story></template>
