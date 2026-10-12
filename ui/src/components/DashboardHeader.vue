<script setup lang="ts">
import { publicAsset } from '../public-asset'
import UiButton from './UiButton.vue'

defineProps<{
  version: string
  uptime: string
}>()

const emit = defineEmits<{
  'open-command-palette': []
  'show-alerts': []
  'create-app': []
}>()
</script>

<template>
  <header class="topbar">
    <a class="brand" href="/" aria-label="FloatLab dashboard">
      <img :src="publicAsset('/assets/logo.svg')" alt="" width="32" height="26" />
      <span>FloatLab</span>
    </a>

    <UiButton
      class="search-trigger"
      variant="plain"
      aria-haspopup="dialog"
      @click="emit('open-command-palette')"
    >
      <img :src="publicAsset('/assets/search.svg')" alt="" width="16" height="16" />
      <span>Search / Cmd Palette</span>
      <kbd>Cmd /</kbd>
    </UiButton>

    <nav class="actions" aria-label="Dashboard actions">
      <UiButton class="alerts" variant="plain" @click="emit('show-alerts')">
        <span aria-hidden="true">●</span> Alerts
      </UiButton>
      <UiButton variant="primary" @click="emit('create-app')">+ New App</UiButton>
    </nav>

    <p class="build-meta">
      <span>{{ version }}</span>
      <span>Uptime {{ uptime }}</span>
    </p>
  </header>
</template>
