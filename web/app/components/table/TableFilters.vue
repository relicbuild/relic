<script setup lang="ts">
const props = defineProps<{ active?: number, matched?: number, total?: number }>()
const emit = defineEmits<{ clear: [] }>()

const matchedAnnouncement = computed(() => {
  if (!props.active || props.matched === undefined) {
    return ''
  }
  return `${props.matched.toLocaleString()}${props.total !== undefined ? ` of ${props.total.toLocaleString()}` : ''} shown`
})
</script>

<template>
  <div class="flex shrink-0 flex-wrap items-center gap-2 border-b border-default bg-default px-4 py-2.5">
    <slot />
    <span role="status" class="sr-only">{{ matchedAnnouncement }}</span>
    <template v-if="active">
      <UButton
        color="neutral"
        variant="ghost"
        size="xs"
        icon="i-lucide-filter-x"
        :label="active === 1 ? 'Clear filter' : 'Clear filters'"
        @click="emit('clear')"
      />
      <span v-if="matched !== undefined" aria-hidden="true" class="text-xs text-muted tabular-nums">
        {{ matched.toLocaleString() }}<template v-if="total !== undefined"> of {{ total.toLocaleString() }}</template>
      </span>
    </template>
    <div v-if="$slots.right" class="ms-auto flex items-center gap-2">
      <slot name="right" />
    </div>
  </div>
</template>
