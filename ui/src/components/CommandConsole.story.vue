<script setup lang="ts">
import CommandConsole from './CommandConsole.vue'
import { fixtureApi } from '../dashboard/live-fixtures'
const args = { api: fixtureApi() }
</script>
<template><Story title="2. Pattern/Command Console"><CommandConsole v-bind="args" /></Story></template>
