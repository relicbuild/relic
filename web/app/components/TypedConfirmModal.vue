<script setup lang="ts">
const props = defineProps<{
  title: string
  description?: string
  name: string
}>()

const emit = defineEmits<{ close: [confirmed: boolean] }>()
const typed = ref('')
</script>

<template>
  <UModal
    :close="{ onClick: () => emit('close', false) }"
    :title="title"
    :description="description"
    :ui="{ footer: 'justify-end' }"
  >
    <template #body>
      <UFormField :label="`Type ${props.name} to confirm`">
        <UInput v-model="typed" autofocus class="w-full" />
      </UFormField>
    </template>
    <template #footer>
      <UButton
        label="Cancel"
        color="neutral"
        variant="outline"
        @click="emit('close', false)"
      />
      <UButton
        label="Delete"
        color="error"
        :disabled="typed !== props.name"
        @click="emit('close', true)"
      />
    </template>
  </UModal>
</template>
