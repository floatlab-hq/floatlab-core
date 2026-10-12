<script setup lang="ts">
import AppDirectory from './AppDirectory.vue'
import { apps } from '../../dashboard/fixtures'
</script>
<template>
  <Story title="2. Pattern/App Directory">
    <Variant title="Interactive"><AppDirectory :apps="apps" /></Variant>
    <Variant title="Read only"><AppDirectory :apps="apps" readonly /></Variant>
  </Story>
</template>
