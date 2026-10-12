<script setup lang="ts">
import { reactive, ref } from 'vue'
import ResourceBar from './ResourceBar.vue'

const value = reactive({ label: '46%', percent: 46 })
const danger = ref(false)
</script>

<template>
  <Story title="1. Primitive/Resource Bar">
    <ResourceBar :value="value" :danger="danger" />

    <template #controls>
      <label>Label <input v-model="value.label" /></label>
      <label>Percent <input v-model.number="value.percent" type="range" min="0" max="100" /></label>
      <label><input v-model="danger" type="checkbox" /> Danger</label>
    </template>
  </Story>
</template>

<style scoped>
.cell-metric { width: 120px; }
label { display: block; margin: 12px; }
</style>
