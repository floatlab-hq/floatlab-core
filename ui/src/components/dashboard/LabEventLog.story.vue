<script setup lang="ts">
import LabEventLog from './LabEventLog.vue'
import { events } from '../../dashboard/fixtures'
</script>
<template>
  <Story title="2. Pattern/Lab Event Log">
    <Variant title="Interactive"><LabEventLog :events="events" /></Variant>
    <Variant title="Read only"><LabEventLog :events="events" readonly /></Variant>
  </Story>
</template>
