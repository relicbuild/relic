<script setup lang="ts">
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { TableColumn } from '@nuxt/ui'
import type { CleanupPolicySchema } from '~/gen/relic/v1/cleanup_pb'
import type { CleanupCandidateView } from '~/utils/view-models'
import { cleanupCandidateView } from '~/utils/mappers'

const props = defineProps<{
  name?: string
  policy?: MessageInitShape<typeof CleanupPolicySchema>
}>()
const emit = defineEmits<{ close: [] }>()
const { cleanup } = useRelic()

const candidates = ref<CleanupCandidateView[]>()
const loading = ref(false)
const error = ref<unknown>()

async function load() {
  loading.value = true
  error.value = undefined
  try {
    const response = await cleanup.previewCleanupPolicy(props.policy ? { policy: props.policy } : { name: props.name })
    candidates.value = response.candidates.map(cleanupCandidateView)
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
}
onMounted(load)

const totalSize = computed(() => (candidates.value ?? []).reduce((sum, c) => sum + c.size, 0))

const columns: TableColumn<CleanupCandidateView>[] = [
  { accessorKey: 'repository', header: 'Repository' },
  { accessorKey: 'path', header: 'Path' },
  { accessorKey: 'size', header: 'Size' },
  { accessorKey: 'created', header: 'Created' },
  { accessorKey: 'lastDownloaded', header: 'Last downloaded' }
]
</script>

<template>
  <USlideover
    :title="name ? `Preview of ${name}` : 'Preview'"
    description="What the policy moves to the trash if it runs now."
    :close="{ onClick: () => emit('close') }"
    :ui="{ content: 'max-w-4xl', body: 'flex flex-col p-0 sm:p-0' }"
  >
    <template #body>
      <div class="flex items-center gap-2 border-b border-default px-4 py-2.5 text-xs text-muted">
        <span v-if="candidates" class="tabular-nums">{{ pluralize(candidates.length, 'item') }}, {{ formatBytes(totalSize) }}</span>
        <UButton
          icon="i-lucide-refresh-cw"
          color="neutral"
          variant="ghost"
          size="sm"
          class="ms-auto"
          aria-label="Refresh"
          :loading="loading"
          @click="load()"
        />
      </div>
      <LoadError
        :error="error"
        what="the preview"
        class="m-4"
        @retry="load()"
      />
      <UTable
        :data="candidates ?? []"
        :columns="columns"
        :loading="loading"
        sticky
        :ui="denseTable"
      >
        <template #repository-cell="{ row }">
          <span class="text-default">{{ row.original.repository }}</span>
        </template>
        <template #path-cell="{ row }">
          <span class="font-mono text-xs text-highlighted break-all">{{ row.original.path }}</span>
        </template>
        <template #size-cell="{ row }">
          <span class="tabular-nums">{{ formatBytes(row.original.size) }}</span>
        </template>
        <template #created-cell="{ row }">
          <span class="text-xs text-muted">{{ formatDate(row.original.created) }}</span>
        </template>
        <template #lastDownloaded-cell="{ row }">
          <span class="text-xs text-muted">{{ row.original.lastDownloaded ? formatDate(row.original.lastDownloaded) : 'Never' }}</span>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-circle-check"
            title="Nothing to clean up"
            description="The policy selects no items now."
          />
        </template>
      </UTable>
    </template>
  </USlideover>
</template>
