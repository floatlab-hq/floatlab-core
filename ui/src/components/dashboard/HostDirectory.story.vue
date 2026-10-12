<script setup lang="ts">
import HostDirectory from './HostDirectory.vue'
import { hosts } from '../../dashboard/fixtures'
</script>

<template>
  <Story title="2. Pattern/Host Directory">
    <HostDirectory :hosts="hosts" />
  </Story>
</template>
