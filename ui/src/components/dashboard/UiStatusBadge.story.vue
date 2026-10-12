<script setup lang="ts">
import UiStatusBadge from './UiStatusBadge.vue'
</script>

<template>
  <Story title="1. Primitive/Status Badge">
    <div class="foundation-row"><UiStatusBadge state="Running" /><UiStatusBadge state="Stopped" /><UiStatusBadge state="Failed" /><UiStatusBadge state="Starting" /><UiStatusBadge state="Updating" /><UiStatusBadge state="Running" disabled /></div>
  </Story>
</template>
