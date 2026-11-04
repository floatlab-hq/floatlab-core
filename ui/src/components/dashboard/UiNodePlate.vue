<script setup lang="ts">
import { IconPencil } from '@tabler/icons-vue'
import UiButton from '../UiButton.vue'
import { nodeCode } from '../../dashboard/node-code'

defineProps<{ name: string; editable?: boolean }>()
</script>

<template>
  <component :is="editable ? UiButton : 'span'" class="node-plate" :class="{ editable }" :title="name" :aria-label="editable ? `Change host ${name}` : `Node ${name}`">
    <IconPencil v-if="editable" class="node-edit-icon" :size="12" aria-hidden="true" />
    <span>{{ nodeCode(name) }}</span>
  </component>
</template>

<style scoped>
.node-plate { display: inline-flex; align-items: center; justify-content: center; gap: var(--space-1); height: var(--height-plate); padding: 0 var(--space-2); border-radius: var(--radius); color: var(--text-secondary); background: var(--surface-canvas); font-size: var(--text-control); font-weight: var(--weight-ui); line-height: var(--line-body); white-space: nowrap; }
.node-edit-icon { display: none; }
.node-plate.editable:hover .node-edit-icon { display: block; }
.node-plate.editable:hover > span { display: none; }
.node-plate { border: 0; min-height: 0; }
</style>
