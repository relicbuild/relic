<script setup lang="ts">
defineProps<{
  folder: boolean
  expanded: boolean
  icon?: string
  iconClass?: unknown
}>()
</script>

<template>
  <UIcon
    v-if="folder"
    name="i-lucide-chevron-right"
    class="size-4 shrink-0 text-dimmed transition-transform duration-200 motion-reduce:transition-none"
    :class="expanded && 'rotate-90'"
  />
  <span v-else class="size-4 shrink-0" />
  <UIcon
    :name="icon ?? (expanded ? 'i-material-icon-theme-folder-base-open' : 'i-material-icon-theme-folder-base')"
    class="size-5 shrink-0"
    :class="iconClass"
  />
</template>
