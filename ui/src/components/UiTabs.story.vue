<script setup lang="ts">
import UiTabs from './UiTabs.vue'
import { ref } from 'vue'
const value = ref('overview')
const tabs = [{ id: 'overview', label: 'Overview' }, { id: 'config', label: 'Config' }, { id: 'disabled', label: 'Unavailable', disabled: true }]
</script>
<template><Story title="1. Primitive/Tabs"><UiTabs v-model="value" :tabs="tabs" label="Detail"><template #default="{ active }">{{ active }}</template></UiTabs></Story></template>
