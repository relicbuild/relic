<script setup lang="ts">
defineProps<{
  title: string
  description?: string
  confirmLabel?: string
}>()

const emit = defineEmits<{
  close: [confirmed: boolean]
}>()
</script>

<template>
  <UModal
    :close="{ onClick: () => emit('close', false) }"
    :title="title"
    :description="description"
    :ui="{ footer: 'justify-end' }"
  >
    <template #footer>
      <UButton
        label="Cancel"
        color="neutral"
        variant="outline"
        @click="emit('close', false)"
      />
      <UButton
        :label="confirmLabel ?? 'Delete'"
        color="error"
        @click="emit('close', true)"
      />
    </template>
  </UModal>
</template>
