<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { ArchiveView } from '~/utils/view-models'
import { ArtifactArchiveEntryViewer } from '#components'

const props = defineProps<{ repo: string, path: string }>()
const { artifacts } = useRelic()
const viewer = useOverlay().create(ArtifactArchiveEntryViewer)
const search = ref('')

const { data: archive, status, error, refresh } = useAsyncData(
  () => `archive:${props.repo}/${props.path}`,
  async () => archiveView(await artifacts.listArchiveEntries({ repository: props.repo, path: props.path })),
  { watch: [() => props.repo, () => props.path] }
)

type Entry = ArchiveView['entries'][number]
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return (archive.value?.entries ?? []).filter(e => !q || e.path.toLowerCase().includes(q))
})

function open(e: Entry) {
  if (e.folder) {
    return
  }
  const base = filePath('archive', props.repo, props.path)
  viewer.open({ url: `${base}?entry=${encodeURIComponent(e.path)}`, entry: e.path })
}

const rowMeta = { class: { tr: (row: { original: Entry }) => row.original.folder ? '' : 'cursor-pointer' } }

const columns: TableColumn<Entry>[] = [
  { accessorKey: 'path', header: 'Entry' },
  { accessorKey: 'size', header: 'Size' },
  { accessorKey: 'modified', header: 'Modified' }
]
</script>

<template>
  <div class="flex flex-col gap-3 py-4">
    <LoadError
      v-if="error"
      :error="error"
      what="the archive"
      @retry="refresh()"
    />
    <template v-else>
      <div class="flex items-center gap-2">
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="Filter entries"
          size="sm"
          class="w-64"
        />
        <span class="ms-auto text-sm text-muted">
          {{ pluralize(rows.length, 'entry') }}{{ archive?.truncated ? ' (first 10,000)' : '' }}
        </span>
      </div>
      <UTable
        :data="rows"
        :columns="columns"
        :loading="status === 'pending'"
        :meta="rowMeta"
        sticky
        class="max-h-[60vh]"
        @select="(_e, row) => open(row.original)"
      >
        <template #path-cell="{ row }">
          <span class="flex items-center gap-2">
            <UIcon :name="row.original.folder ? 'i-lucide-folder' : 'i-lucide-file'" class="size-4 shrink-0 text-muted" />
            <span class="font-mono text-xs break-all">{{ row.original.path }}</span>
          </span>
        </template>
        <template #size-cell="{ row }">
          {{ row.original.folder ? '—' : formatBytes(row.original.size) }}
        </template>
        <template #modified-cell="{ row }">
          {{ formatDate(row.original.modified) }}
        </template>
      </UTable>
    </template>
  </div>
</template>
