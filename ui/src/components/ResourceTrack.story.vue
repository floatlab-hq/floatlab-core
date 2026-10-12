<script setup lang="ts">
import { ref } from 'vue'
import ResourceTrack from './ResourceTrack.vue'

const percent = ref(46)
const danger = ref(false)
const history = ref(false)
const reverse = ref(false)
const compact = ref(false)
</script>

<template>
  <Story title="1. Primitive/Resource Track">
    <div class="preview">
      <ResourceTrack
        :percent="percent"
        :history="history ? [20, 46, 34, 52] : undefined"
        :danger="danger"
        :reverse="reverse"
        :compact="compact"
      />
    </div>

    <template #controls>
      <label><input v-model="compact" type="checkbox" /> Compact</label>
      <label>Percent <input v-model.number="percent" type="range" min="0" max="100" /></label>
      <label><input v-model="danger" type="checkbox" /> Danger</label>
      <label><input v-model="history" type="checkbox" /> History</label>
      <label><input v-model="reverse" type="checkbox" /> Reverse</label>
    </template>
  </Story>
</template>

<style scoped>
.preview { position: relative; min-height: 90px; }
label { display: block; margin: 12px; }
</style>
