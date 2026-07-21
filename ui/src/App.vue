<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "./api/client";

const status = ref("Connecting…");

onMounted(async () => {
  const { error } = await api.GET("/health");
  status.value = error ? "API unavailable" : "API connected";
});
</script>

<template>
  <main>
    <h1>FloatLab</h1>
    <p>{{ status }}</p>
  </main>
</template>
