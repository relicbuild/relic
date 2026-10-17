<script setup lang="ts">
const props = withDefaults(defineProps<{ total: number, noun?: string }>(), { noun: 'rows' })
const page = defineModel<number>('page', { required: true })
const pageSize = defineModel<number>('pageSize', { required: true })

const sizes = [20, 50, 100, 200]
const start = computed(() => Math.min((page.value - 1) * pageSize.value + 1, props.total))
const end = computed(() => Math.min(page.value * pageSize.value, props.total))

watch([() => props.total, pageSize], () => {
  const last = Math.max(1, Math.ceil(props.total / pageSize.value))
  if (page.value > last) {
    page.value = last
  }
})
</script>

<template>
  <div class="flex shrink-0 items-center justify-between gap-3 border-t border-default bg-default px-4 py-2.5 text-xs text-muted">
    <div class="flex items-center gap-3">
      <span v-if="total === 0">No {{ noun }}</span>
      <span v-else class="tabular-nums">
        <span class="font-medium text-highlighted">{{ start.toLocaleString() }}</span>–<span class="font-medium text-highlighted">{{ end.toLocaleString() }}</span>
        of <span class="font-medium text-highlighted">{{ total.toLocaleString() }}</span> {{ noun }}
      </span>
      <span class="text-dimmed">·</span>
      <label class="inline-flex items-center gap-1.5">
        Per page
        <USelect
          :model-value="pageSize"
          :items="sizes"
          size="xs"
          @update:model-value="n => { pageSize = Number(n); page = 1 }"
        />
      </label>
    </div>
    <UPagination
      v-model:page="page"
      :total="total"
      :items-per-page="pageSize"
      show-edges
      size="xs"
    />
  </div>
</template>
