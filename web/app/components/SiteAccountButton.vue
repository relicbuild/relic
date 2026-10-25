<script setup lang="ts">
const props = defineProps<{ compact?: boolean }>()
const { me } = useSession()
const route = useRoute()
const ui = computed(() => props.compact ? { label: 'hidden sm:inline' } : undefined)
</script>

<template>
  <UButton
    v-if="me"
    to="/dashboard"
    icon="i-lucide-layout-dashboard"
    label="Open the app"
    :aria-label="compact ? 'Open the app' : undefined"
    :ui="ui"
  />
  <UButton
    v-else-if="route.path !== '/login'"
    to="/login"
    icon="i-lucide-log-in"
    label="Sign in"
    :aria-label="compact ? 'Sign in' : undefined"
    :ui="ui"
  />
</template>
