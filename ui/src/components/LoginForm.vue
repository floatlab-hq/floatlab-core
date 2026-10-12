<script setup lang="ts">
import { ref } from 'vue'
import type { LoginRequest } from '@floatlab/client'
import UiInput from './UiInput.vue'
import UiButton from './UiButton.vue'
defineProps<{ busy?: boolean; error?: string }>()
const emit = defineEmits<{ submit: [credentials: LoginRequest] }>()
const username = ref('')
const password = ref('')
function submit() { emit('submit', { username: username.value, password: password.value }); password.value = '' }
</script>
<template>
  <main class="login-screen">
    <form @submit.prevent="submit">
      <h1>FloatLab</h1><h2>Sign in</h2>
      <label for="username">Username</label>
      <UiInput id="username" v-model="username" variant="field" autocomplete="username" required :disabled="busy" />
      <label for="password">Password</label>
      <UiInput id="password" v-model="password" variant="field" type="password" autocomplete="current-password" required :disabled="busy" />
      <p v-if="error" role="alert">{{ error }}</p>
      <UiButton type="submit" :disabled="busy">{{ busy ? 'Signing in…' : 'Sign in' }}</UiButton>
    </form>
  </main>
</template>
<style scoped>
.login-screen { display: grid; place-items: center; min-height: 100vh; padding: 24px; }
form { display: grid; gap: 16px; width: min(360px, 100%); padding: 24px; background: var(--surface-recessed); }
h1, h2 { margin: 0; } [role="alert"] { color: var(--status-error); }
</style>
