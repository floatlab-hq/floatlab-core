<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import UiFlipCount from '../UiFlipCount.vue'
import { IconSettings, IconSparklesFilled } from '@tabler/icons-vue'
import UiButton from '../UiButton.vue'
import UiAssetIcon from './UiAssetIcon.vue'

const props = withDefaults(defineProps<{ errorCount?: number; warningCount?: number; eventsOpen?: boolean }>(), { errorCount: 0, warningCount: 0, eventsOpen: false })
const eventSummary = computed(() => `${props.errorCount} recent errors, ${props.warningCount} recent warnings`)
const emit = defineEmits<{ (event: 'command' | 'settings' | 'events' | 'nodeKey' | 'logout'): void }>()
const settingsOpen = ref(false)
const settingsRoot = ref<HTMLElement>()
const settingsId = useId()
function closeSettings(focus = false) {
  settingsOpen.value = false
  if (focus) settingsRoot.value?.querySelector('button')?.focus()
}
function outside(event: globalThis.PointerEvent) {
  if (!settingsRoot.value?.contains(event.target as globalThis.Node)) closeSettings()
}
function focusOut(event: globalThis.FocusEvent) {
  if (!settingsRoot.value?.contains(event.relatedTarget as globalThis.Node)) closeSettings()
}
function selectSetting(action: 'settings' | 'nodeKey' | 'logout') {
  closeSettings(true)
  emit(action)
}
onMounted(() => document.addEventListener('pointerdown', outside))
onBeforeUnmount(() => document.removeEventListener('pointerdown', outside))
</script>

<template>
  <header class="dashboard-navigation">
    <a class="dashboard-brand" href="#dashboard" aria-label="FloatLab dashboard">
      <UiAssetIcon src="/assets/dashboard/imgFloatLabLogo.svg" :size="34" />
      <span>FloatLab</span>
    </a>
    <div class="dashboard-command-container">
      <slot name="command">
        <UiButton class="dashboard-command" variant="surface" aria-haspopup="dialog" aria-label="Command Palette" @click="$emit('command')">
          <span class="command-title">Command Palette <span class="command-hint">Search apps, hosts, actions, cli and more</span></span>
          <IconSparklesFilled :size="12" aria-hidden="true" />
        </UiButton>
      </slot>
    </div>
    <UiButton variant="surface" class="dashboard-events" :class="{ 'has-errors': errorCount > 0, 'has-warnings': !errorCount && warningCount > 0 }" :aria-label="`Events: ${eventSummary}`" :aria-expanded="eventsOpen" :title="eventSummary" @click="$emit('events')">
      <span>Events</span><UiFlipCount :count="errorCount || warningCount" />
    </UiButton>
    <div ref="settingsRoot" class="dashboard-settings-container" @keydown.esc.stop="closeSettings(true)" @focusout="focusOut">
      <UiButton variant="surface" class="dashboard-settings" aria-label="Settings" :aria-expanded="settingsOpen" :aria-controls="settingsId" @click="settingsOpen = !settingsOpen"><IconSettings :size="20" aria-hidden="true" /></UiButton>
      <div v-if="settingsOpen" :id="settingsId" class="dashboard-settings-dropdown" role="group" aria-label="Settings actions">
        <UiButton variant="ghost" @click="selectSetting('settings')">Alert rules</UiButton>
        <UiButton variant="ghost" @click="selectSetting('nodeKey')">Node key</UiButton>
        <UiButton variant="ghost" @click="selectSetting('logout')">Sign out</UiButton>
      </div>
    </div>
  </header>
</template>

<style scoped>
.dashboard-navigation { display: flex; align-items: center; gap: var(--space-3); height: var(--height-control); }
.dashboard-brand { display: inline-flex; align-items: center; gap: var(--space-3); color: var(--text-primary); text-decoration: none; font-size: var(--text-heading); line-height: var(--line-heading); }
.dashboard-brand .asset-icon { color: var(--omarchy-accent); }
.dashboard-command-container { flex: 1; min-width: 0; }
.dashboard-command { width: 100%; justify-content: space-between; min-width: 0; text-align: left; }
.command-hint { color: var(--text-placeholder); }
.dashboard-events { gap: 10px; }
.dashboard-events.has-errors { color: var(--status-error); }
.dashboard-events.has-warnings { color: var(--status-warning); }
.dashboard-settings-container { position: relative; }
.dashboard-settings { width: var(--height-control); padding: 0; }
.dashboard-settings-dropdown { position: absolute; z-index: 5; right: 0; top: calc(100% + 8px); display: grid; min-width: 160px; padding: 4px; background: var(--surface-recessed); border: 1px solid var(--line); border-radius: var(--radius); }
.dashboard-settings-dropdown .button { text-align: left; }
@media (max-width: 800px) { .command-hint { display: none; } }
@media (max-width: 500px) { .command-title { display: none; } .dashboard-command { padding: 0; justify-content: center; } }
</style>
