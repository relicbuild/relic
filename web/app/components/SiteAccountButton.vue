<script setup lang="ts">
const { me } = useSession()
const route = useRoute()
</script>

<template>
  <UButton
    v-if="me"
    to="/dashboard"
    icon="i-lucide-layout-dashboard"
    label="Open the app"
  />
  <UButton
    v-else-if="route.path !== '/login'"
    to="/login"
    icon="i-lucide-log-in"
    label="Sign in"
  />
</template>
