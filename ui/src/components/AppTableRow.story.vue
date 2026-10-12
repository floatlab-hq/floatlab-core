<script setup lang="ts">
import { computed, ref } from 'vue'
import { apps } from '../data'
import type { Operation } from '../types'
import AppTableRow from './AppTableRow.vue'

const selected = ref(0)
const app = computed(() => apps[selected.value])
const lastOperation = ref<Operation>()
</script>

<template>
  <Story title="2. Pattern/App Table Row" responsive-disabled>
    <table>
      <colgroup>
        <col class="app-col" /><col class="state-col" /><col class="info-col" />
        <col v-for="column in 5" :key="column" class="resource-col" />
      </colgroup>
      <tbody>
        <AppTableRow :app="app" @operation="lastOperation = $event" />
      </tbody>
    </table>
    <p>Selected operation: {{ lastOperation ?? 'none' }}</p>

    <template #controls>
      <label>
        App
        <select v-model.number="selected">
          <option v-for="(item, index) in apps" :key="item.id" :value="index">{{ item.name }}</option>
        </select>
      </label>
    </template>
  </Story>
</template>

<style scoped>
table { margin-top: 0; }
p { margin: 16px; }
label { display: block; margin: 12px; }
</style>
