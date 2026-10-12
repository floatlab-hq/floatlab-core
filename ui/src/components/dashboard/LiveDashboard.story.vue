<script setup lang="ts">
import LiveDashboard from './LiveDashboard.vue'
import { fixtureApi } from '../../dashboard/live-fixtures'
const args = { api: fixtureApi() }
</script>
<template><Story title="2. Pattern/Live Dashboard"><LiveDashboard v-bind="args" /></Story></template>
