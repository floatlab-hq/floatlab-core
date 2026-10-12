<script setup lang="ts">
import LiveHostDetail from './LiveHostDetail.vue'
import { node } from '../../dashboard/live-fixtures'
const args = { node, apps: [] }
</script>
<template><Story title="2. Pattern/Live Host Detail"><LiveHostDetail v-bind="args" /></Story></template>
