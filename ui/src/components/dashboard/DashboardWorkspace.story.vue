<script setup lang="ts">
import DashboardWorkspace from './DashboardWorkspace.vue'
import { hosts, apps, events } from '../../dashboard/fixtures'

</script>
<template><Story title="2. Pattern/Dashboard Workspace"><DashboardWorkspace :hosts="hosts" :apps="apps" :events="events" /></Story></template>
