<script setup lang="ts">
import { IconSparklesFilled } from '@tabler/icons-vue'
import type { LabEvent } from '../../dashboard/types'
import UiButton from '../UiButton.vue'
import UiNodePlate from './UiNodePlate.vue'

defineProps<{ events: LabEvent[]; readonly?: boolean }>()
defineEmits<{ select: [eventId: string] }>()
const labels = { error: 'ERR', info: 'INFO', warning: 'WARN' }
</script>

<template>
  <section class="lab-event-log" aria-label="Recent lab events">
    <div v-for="event in events" :key="event.id" class="lab-event-row" :class="event.tone">
      <span>{{ labels[event.tone] }}</span><time>{{ event.time }}</time>
      <UiNodePlate :name="event.host" />
      <span class="event-source">{{ event.source }}</span><span class="event-message">{{ event.message }}</span>
      <UiButton v-if="event.tone === 'error' && !readonly" variant="plain" class="event-action" :aria-label="`Inspect ${event.source} event`" @click="$emit('select', event.id)"><IconSparklesFilled :size="12" aria-hidden="true" /></UiButton>
    </div>
  </section>
</template>

<style scoped>
.lab-event-log { display: grid; gap: var(--space-2); margin-top: var(--space-2); }
.lab-event-row { display: grid; grid-template-columns: 35px 160px auto 102px minmax(0, 1fr) 12px; align-items: center; gap: var(--space-4); min-height: var(--height-plate); padding: 0 var(--space-4); border-radius: var(--radius); background: var(--surface-default); color: var(--text-secondary); font-size: var(--text-control); line-height: var(--line-body); }
.error { color: var(--status-error); }
.warning { color: var(--status-warning); font-style: normal; }
.event-message { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lab-event-row time { overflow-wrap: anywhere; font-size: var(--text-meta); }
.event-action { color: var(--text-secondary); }
@media (max-width: 800px) { .lab-event-row { grid-template-columns: 35px 100px auto minmax(0, 1fr) 12px; } .event-source { display: none; } }

@container (max-width: 700px) { .lab-event-row { grid-template-columns: 40px minmax(0,1fr) 12px; gap: 8px; padding: 0 8px; } .lab-event-row > :first-child, .lab-event-row :deep(.node-plate), .event-source { display: none; } }
</style>
