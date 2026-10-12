<script setup lang="ts">
import { publicAsset } from '../public-asset'
import { computed, ref } from 'vue'
import type { AppRecord, Operation, SortKey } from '../types'
import AppTableRow from './AppTableRow.vue'
import UiButton from './UiButton.vue'

const props = defineProps<{ apps: AppRecord[] }>()
const emit = defineEmits<{
  operation: [appId: string, operation: Operation]
}>()

const columns: { key: SortKey; label: string }[] = [
  { key: 'name', label: 'Apps' },
  { key: 'status', label: 'State' },
  { key: 'info', label: 'Info' },
  { key: 'cpu', label: 'CPU' },
  { key: 'memory', label: 'Mem' },
  { key: 'storage', label: 'Storage' },
  { key: 'disk', label: 'Disk IO' },
  { key: 'network', label: 'Network' },
]
const sortKey = ref<SortKey>('name')
const sortDirection = ref<'ascending' | 'descending'>('ascending')

const infoText = (app: AppRecord) =>
  [app.links?.join(' '), app.address, app.notice].filter(Boolean).join(' ')

const sortedApps = computed(() =>
  [...props.apps].sort((a, b) => {
    let comparison: number

    if (sortKey.value === 'name' || sortKey.value === 'status') {
      comparison = a[sortKey.value].localeCompare(b[sortKey.value])
    } else if (sortKey.value === 'info') {
      comparison = infoText(a).localeCompare(infoText(b))
    } else if (sortKey.value === 'network') {
      comparison = a.network.up.percent + a.network.down.percent - b.network.up.percent - b.network.down.percent
    } else {
      comparison = a[sortKey.value].percent - b[sortKey.value].percent
    }

    return sortDirection.value === 'ascending' ? comparison : -comparison
  }),
)

const sortBy = (key: SortKey) => {
  if (sortKey.value === key) {
    sortDirection.value = sortDirection.value === 'ascending' ? 'descending' : 'ascending'
  } else {
    sortKey.value = key
    sortDirection.value = 'ascending'
  }
}
</script>

<template>
  <table>
    <colgroup>
      <col class="app-col" />
      <col class="state-col" />
      <col class="info-col" />
      <col v-for="key in ['cpu', 'memory', 'storage', 'disk', 'network']" :key="key" class="resource-col" />
    </colgroup>
    <thead>
      <tr>
        <th
          v-for="column in columns"
          :key="column.key"
          :aria-sort="sortKey === column.key ? sortDirection : undefined"
        >
          <UiButton class="column-sort" @click="sortBy(column.key)">
            {{ column.key === 'name' ? `${apps.length} Apps` : column.label }}
            <img :src="publicAsset('/assets/chevron-down.svg')" alt="" />
          </UiButton>
        </th>
      </tr>
    </thead>
    <tbody>
      <AppTableRow
        v-for="app in sortedApps"
        :key="app.id"
        :app="app"
        @operation="(operation) => emit('operation', app.id, operation)"
      />
    </tbody>
  </table>
</template>
