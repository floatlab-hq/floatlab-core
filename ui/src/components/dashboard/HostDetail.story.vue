<script setup lang="ts">
import HostDetail from './HostDetail.vue'
import { hosts, apps } from '../../dashboard/fixtures'
const host = hosts[0]!
</script>
<template><Story title="2. Pattern/Host Detail"><HostDetail :host="host" :apps="apps" /></Story></template>
