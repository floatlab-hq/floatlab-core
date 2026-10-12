<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { AppRecord, Operation, Server } from '../types'
import AppTable from './AppTable.vue'
import ServerSummary from './ServerSummary.vue'

defineProps<{ server: Server; apps: AppRecord[] }>()
const emit = defineEmits<{
  settings: []
  operation: [appId: string, operation: Operation]
}>()

const tableScroll = ref<HTMLElement>()
const canScrollLeft = ref(false)
const canScrollRight = ref(false)

function updateScroll() {
  if (!tableScroll.value) return

  const { scrollLeft, clientWidth, scrollWidth } = tableScroll.value
  canScrollLeft.value = scrollLeft > 1
  canScrollRight.value = scrollLeft + clientWidth < scrollWidth - 1
}

function forwardOperation(appId: string, operation: Operation) {
  emit('operation', appId, operation)
}

onMounted(updateScroll)
</script>

<template>
  <main>
    <section
      class="server-panel"
      :class="{ 'can-scroll-left': canScrollLeft, 'can-scroll-right': canScrollRight }"
      aria-labelledby="server-name"
    >
      <div ref="tableScroll" class="table-scroll" @scroll="updateScroll">
        <ServerSummary :server="server" @settings="emit('settings')" />
        <AppTable :apps="apps" @operation="forwardOperation" />
      </div>
    </section>
  </main>
</template>
