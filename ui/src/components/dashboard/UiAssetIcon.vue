<script setup lang="ts">
import { publicAsset } from '../../public-asset'
withDefaults(defineProps<{ src: string; size?: number }>(), { size: 20 })
</script>

<template>
  <span class="asset-icon" aria-hidden="true" :style="{ width: `${size}px`, height: `${size}px`, maskImage: `url(${publicAsset(src)})` }"></span>
</template>

<style scoped>
.asset-icon { display: inline-block; flex: 0 0 auto; background: currentColor; mask-size: contain; mask-repeat: no-repeat; mask-position: center; }
</style>
