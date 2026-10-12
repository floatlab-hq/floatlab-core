<script setup lang="ts">
import UiTelemetry from './UiTelemetry.vue'
</script>

<template>
  <Story title="1. Primitive/Telemetry">
    <Variant title="Available"><UiTelemetry cpu="⠴⠼⠦" :memory="38" :disk="40" /></Variant>
    <Variant title="Unavailable"><UiTelemetry cpu="—" /></Variant>
  </Story>
</template>
