<script setup lang="ts">
import ApiForm from './ApiForm.vue'
import { ref } from 'vue'
import { coreForms } from '../forms'
import type { FormValues } from '../forms/schema'
const values = ref<FormValues>({})
const schema = coreForms.NodeCreate
</script>
<template><Story title="2. Pattern/API Form"><ApiForm v-model="values" :schema="schema" /><pre>{{ values }}</pre></Story></template>
