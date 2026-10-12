<script setup lang="ts">
import { onBeforeUnmount, ref, shallowRef } from 'vue'
import { ApiError, type LoginRequest } from '@floatlab/client'
import { browserApi } from './api/client'
import LoginForm from './components/LoginForm.vue'
import LiveDashboard from './components/dashboard/LiveDashboard.vue'
const session = shallowRef<ReturnType<typeof browserApi>>()
const busy = ref(false)
const error = ref('')
let expiry: ReturnType<typeof setTimeout> | undefined
let generation = 0
function logout() { generation++; clearTimeout(expiry); session.value = undefined; busy.value = false }
async function login(credentials: LoginRequest) {
  busy.value = true; error.value = ''
  const request = ++generation
  try {
    const token = await browserApi().createAuthToken(credentials)
    if (request !== generation) return
    session.value = browserApi(token.access_token)
    expiry = setTimeout(logout, token.expires_in * 1000)
  } catch (cause) {
    if (request === generation) error.value = cause instanceof ApiError ? cause.message : 'Could not sign in'
  } finally { if (request === generation) busy.value = false }
}
onBeforeUnmount(logout)
</script>
<template>
  <LiveDashboard v-if="session" :api="session" @logout="logout" />
  <LoginForm v-else :busy="busy" :error="error" @submit="login" />
</template>
