<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { TrashEntryView } from '~/utils/view-models'
import { trashEntryView } from '~/utils/mappers'
import { ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { trash } = useRelic()
const toast = useToast()
const confirm = useOverlay().create(ConfirmModal)
const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const query = useDebounced(search)
const repository = ref('all')
const deletedBy = ref('all')
const kind = ref('all')
const filters = [query, repository, deletedBy, kind]
watch(filters, () => {
  page.value = 1
})
const active = computed(() => [search.value.trim() !== '', repository.value !== 'all', deletedBy.value !== 'all', kind.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  repository.value = deletedBy.value = kind.value = 'all'
}

const any = (v: string) => (v === 'all' ? '' : v)
const { data, status, refresh, error } = await useAsyncData(
  'admin:trash',
  async () => {
    const r = await trash.listTrash({
      page: page.value, pageSize: pageSize.value, query: query.value.trim(), originalRepository: any(repository.value),
      deletedBy: any(deletedBy.value), kind: any(kind.value)
    })
    return { entries: r.entries.map(trashEntryView), total: toNumber(r.total), repositories: r.originalRepositories, deleters: r.deletedByUsers }
  },
  { watch: [page, pageSize, ...filters] }
)

const repositoryItems = computed(() => [
  { label: 'All repositories', value: 'all' },
  ...(data.value?.repositories ?? []).map(r => ({ label: r, value: r }))
])
const deletedByItems = computed(() => [
  { label: 'Deleted by anyone', value: 'all' },
  ...(data.value?.deleters ?? []).map(u => ({ label: u, value: u }))
])
const kindItems = [
  { label: 'Files and folders', value: 'all' },
  { label: 'Files', value: 'file' },
  { label: 'Folders', value: 'folder' }
]

async function restore(e: TrashEntryView) {
  try {
    await trash.restoreTrash({ path: e.path })
    toast.add({ title: `${e.name} restored to ${e.originalRepo}/${e.originalPath}`, color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: `Could not restore ${e.name}`, description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function remove(e: TrashEntryView) {
  if (!(await confirm.open({ title: `Delete ${e.name} permanently?`, description: 'It cannot be restored afterwards.' }).result)) {
    return
  }
  try {
    await trash.deleteTrashEntry({ path: e.path })
    await refresh()
  } catch (err) {
    toast.add({ title: `Could not delete ${e.name}`, description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function empty() {
  if (!(await confirm.open({ title: 'Empty the trash can?', description: 'Everything in it is deleted permanently.', confirmLabel: 'Empty' }).result)) {
    return
  }
  try {
    await trash.emptyTrash({})
    toast.add({ title: 'Trash can emptied', color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not empty the trash can', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns: TableColumn<TrashEntryView>[] = [
  { accessorKey: 'originalPath', header: 'Deleted item' },
  { accessorKey: 'size', header: 'Size' },
  { accessorKey: 'deletedBy', header: 'Deleted by' },
  { accessorKey: 'deleted', header: 'Deleted' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-trash" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Trash can">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(data.total, 'item') }}</span>
        </template>
        <template #right>
          <UButton
            icon="i-lucide-folder-tree"
            color="neutral"
            variant="outline"
            label="Browse"
            to="/repositories/auto-trashcan"
          />
          <UButton
            v-if="administers"
            icon="i-lucide-trash-2"
            color="error"
            label="Empty trash"
            :disabled="!active && !data?.total"
            @click="empty"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters :active="active" :matched="data?.total" @clear="clearFilters">
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="Path or user…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelectMenu
          v-model="repository"
          :items="repositoryItems"
          value-key="value"
          icon="i-lucide-box"
          size="sm"
          class="w-48"
        />
        <USelectMenu
          v-model="deletedBy"
          :items="deletedByItems"
          value-key="value"
          icon="i-lucide-user"
          size="sm"
          class="w-44"
        />
        <USelect
          v-model="kind"
          aria-label="Filter by kind"
          :items="kindItems"
          size="sm"
          class="w-40"
        />
        <template #right>
          <span class="text-xs text-muted">Kept for 14 days by default (RELIC_TRASH_RETENTION).</span>
        </template>
      </TableFilters>

      <LoadError
        :error="error"
        what="the trash can"
        class="m-4"
        @retry="refresh()"
      />
      <UTable
        :data="data?.entries ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        sticky
        :ui="denseTable"
      >
        <template #originalPath-cell="{ row }">
          <div class="flex items-center gap-2">
            <UIcon :name="row.original.folder ? 'i-lucide-folder' : 'i-lucide-file'" class="size-4 shrink-0 text-muted" />
            <div class="min-w-0">
              <div class="truncate font-medium text-highlighted">
                {{ row.original.name }}
              </div>
              <div class="truncate text-xs text-muted">
                {{ row.original.originalRepo }}/{{ row.original.originalPath }}
              </div>
            </div>
          </div>
        </template>
        <template #size-cell="{ row }">
          {{ row.original.folder ? '—' : formatBytes(row.original.size) }}
        </template>
        <template #deleted-cell="{ row }">
          {{ formatDate(row.original.deleted) }}
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-undo-2"
              color="neutral"
              variant="ghost"
              label="Restore"
              @click="restore(row.original)"
            />
            <UButton
              icon="i-lucide-trash"
              color="error"
              variant="ghost"
              aria-label="Delete permanently"
              @click="remove(row.original)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-trash-2"
            :title="active ? 'No deleted item matches these filters' : 'The trash can is empty'"
            :description="active ? undefined : 'Deleted files and folders appear here until they expire.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="data?.total ?? 0"
        noun="items"
      />
    </template>
  </UDashboardPanel>
</template>
