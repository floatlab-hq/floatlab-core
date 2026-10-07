<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "./api/client";
import CommandPalette from "./components/CommandPalette.vue";

const status = ref("Connecting…");

onMounted(async () => {
  try {
    const response = await fetch("/api/v1/health");
    status.value = response.ok ? "API connected" : "API unavailable";
  } catch {
    status.value = "API unavailable";
  }
});
</script>

<template>
  <main>
    <h1>FloatLab</h1>
    <p>{{ status }}</p>
  </main>
  <CommandPalette :api="api" />
</template>
