<script setup lang="ts">
import LabDashboard from './LabDashboard.vue'
import { hosts, apps, events } from '../../dashboard/fixtures'
</script>

<template>
  <Story title="2. Pattern/Lab Dashboard">
    <LabDashboard :hosts="hosts" :apps="apps" :events="events" />
  </Story>
</template>
