<script setup lang="ts">
import { IconSparklesFilled } from '@tabler/icons-vue'
import UiButton from '../UiButton.vue'
import UiAssetIcon from './UiAssetIcon.vue'

defineEmits<{ command: []; settings: [] }>()
</script>

<template>
  <header class="dashboard-navigation">
    <a class="dashboard-brand" href="#dashboard" aria-label="FloatLab dashboard">
      <UiAssetIcon src="/assets/dashboard/imgFloatLabLogo.svg" :size="34" />
      <span>FloatLab</span>
    </a>
    <UiButton class="dashboard-command" variant="surface" aria-haspopup="dialog" aria-label="Command Palette" @click="$emit('command')">
      <span class="command-title">Command Palette <span class="command-hint">Search apps, hosts, actions, cli and more</span></span>
      <IconSparklesFilled :size="12" aria-hidden="true" />
    </UiButton>
    <UiButton variant="surface" class="dashboard-settings" @click="$emit('settings')">Settings</UiButton>
  </header>
</template>

<style scoped>
.dashboard-navigation { display: flex; align-items: center; gap: var(--space-3); height: var(--height-control); }
.dashboard-brand { display: inline-flex; align-items: center; gap: var(--space-3); color: var(--text-primary); text-decoration: none; font-size: var(--text-heading); line-height: var(--line-heading); }
.dashboard-brand .asset-icon { color: var(--omarchy-accent); }
.dashboard-command { flex: 1; justify-content: space-between; min-width: 0; text-align: left; }
.command-hint { color: var(--text-placeholder); }
.dashboard-settings { width: 100px; }
@media (max-width: 800px) { .command-hint { display: none; } }
@media (max-width: 500px) { .command-title { display: none; } .dashboard-command { flex: 0 0 40px; padding: 0; justify-content: center; } .dashboard-brand { margin-right: auto; } .dashboard-settings { width: auto; } }
</style>
