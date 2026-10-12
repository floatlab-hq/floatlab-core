<script setup lang="ts">
import { IconDotsFilled, IconXFilled } from '@tabler/icons-vue'
import type { HostItem } from '../../dashboard/types'
import UiButton from '../UiButton.vue'
import UiNodePlate from './UiNodePlate.vue'
import UiHealthMark from './UiHealthMark.vue'
import UiTelemetry from './UiTelemetry.vue'

defineProps<{ hosts: HostItem[] }>()
defineEmits<{ tools: [hostId: string]; inspect: [hostId: string]; connect: [hostId: string, target: 'network' | 'host'] }>()
</script>

<template>
  <div class="directory-scroll">
    <div class="host-directory" role="table" aria-label="Hosts">
      <div class="host-columns" role="row">
        <span v-for="label in ['State', 'Badge', 'Name', 'Network', 'Host', 'Hosting', 'HA target', 'Backup target', '']" :key="label" role="columnheader">{{ label }}</span>
      </div>
      <div v-for="host in hosts" :key="host.id" class="host-row" role="row">
        <span class="host-state" role="cell"><UiHealthMark :state="host.health" /></span>
        <span role="cell"><UiNodePlate :name="host.name" /></span>
        <div class="host-identity" role="cell">
          <span class="host-name">{{ host.name }}</span>
          <span v-if="host.health !== 'ready'" class="host-app-count">{{ host.apps }} apps</span>
          <UiTelemetry :cpu="host.cpu" :memory="host.memory" :disk="host.disk" />
          <UiButton variant="plain" class="host-inspect" :aria-label="`Inspect ${host.name}`" @click="$emit('inspect', host.id)"><IconDotsFilled :size="14" aria-hidden="true" /></UiButton>
        </div>
        <div role="cell"><UiButton v-if="host.network" variant="plain" class="directory-link" @click="$emit('connect', host.id, 'network')">{{ host.network }}</UiButton></div>
        <div role="cell"><UiButton v-if="host.address" variant="plain" class="directory-link" @click="$emit('connect', host.id, 'host')">{{ host.address }}</UiButton></div>
        <span role="cell"><span v-if="host.health === 'ready'">{{ host.apps }} apps</span></span>
        <span role="cell"><span v-if="host.standby">{{ host.standby }} standby</span></span>
        <span role="cell"><IconXFilled v-if="host.backupAttention" :size="20" role="img" aria-label="Backup attention" /></span>
        <span role="cell"><UiButton variant="plain" :aria-label="`Tools for ${host.name}`" @click="$emit('tools', host.id)"><IconDotsFilled :size="14" aria-hidden="true" /></UiButton></span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.directory-scroll { overflow-x: auto; container-type: inline-size; }
.host-directory { min-width: 1300px; }
.host-columns, .host-row { display: grid; grid-template-columns: 48px 80px 395px 170px 148px 150px 180px minmax(85px, 1fr) 44px; align-items: center; }
.host-columns { height: 42px; color: var(--text-secondary); font-size: var(--text-column); line-height: var(--line-column); letter-spacing: var(--tracking-column); text-transform: uppercase; }
.host-row { min-height: var(--height-host-row); margin-bottom: var(--space-2); border-radius: var(--radius); background: var(--surface-default); font-size: var(--text-meta); line-height: var(--line-item); }
.host-row:last-child { margin-bottom: 0; }
.host-state { display: flex; justify-content: center; }
.host-identity { display: flex; align-items: center; gap: var(--space-3); padding: 0 14px; }
.host-name { color: var(--text-primary); font-size: var(--text-item); }
.host-inspect { margin-left: auto; }
.host-row > [role="cell"] { min-width: 0; }
.directory-link { color: var(--text-secondary); font-size: var(--text-status); text-decoration: underline; }

@container (max-width: 700px) {
.host-directory { min-width: 0; }
.host-columns, .host-row { grid-template-columns: 28px 70px minmax(0,1fr) 30px; }
.host-columns > :nth-child(n+4):not(:last-child), .host-row > :nth-child(n+4):not(:last-child) { display: none; }
.host-identity { flex-wrap: wrap; gap: 4px 8px; padding: 8px; }
.host-identity :deep(.telemetry) { flex-basis: 100%; }
.host-inspect { display: none; }
}
</style>
