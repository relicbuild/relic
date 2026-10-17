<script setup lang="ts">
defineProps<{ url: string, entry: string }>()
const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <USlideover :title="entry" :close="{ onClick: () => emit('close') }" :ui="{ content: 'max-w-3xl' }">
    <template #body>
      <ArtifactFileViewer :url="url" :name="entry" />
    </template>
  </USlideover>
</template>
