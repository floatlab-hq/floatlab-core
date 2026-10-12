<script setup lang="ts">
import SplitView from './SplitView.vue'
import { ref } from 'vue'
import UiButton from './UiButton.vue'
const open = ref(false)
</script>
<template><Story title="3. Layout/Split View"><UiButton variant="surface" @click="open = !open">Toggle panel</UiButton><SplitView :open="open"><p>Primary content</p><template #detail><p>Detail content</p></template></SplitView></Story></template>
