<script setup lang="ts">
import UiHealthMark from './UiHealthMark.vue'
</script>

<template>
  <Story title="1. Primitive/Health Mark">
    <div class="foundation-row"><UiHealthMark state="ready" /> Ready <UiHealthMark state="stopped" /> Stopped <UiHealthMark state="standby" /> Standby <UiHealthMark state="degraded" /> Degraded <UiHealthMark state="unknown" /> Unknown</div>
  </Story>
</template>
