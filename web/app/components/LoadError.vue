<script setup lang="ts">
defineProps<{
  error: unknown
  what: string
}>()
const emit = defineEmits<{ retry: [] }>()
</script>

<template>
  <UAlert
    v-if="error"
    color="error"
    variant="subtle"
    icon="i-lucide-circle-alert"
    :title="`Could not load ${what}`"
    :description="relicErrorMessage(error, 'The server could not be reached.')"
    :actions="[{ label: 'Retry', icon: 'i-lucide-refresh-cw', color: 'error', variant: 'outline', onClick: () => emit('retry') }]"
  />
</template>
