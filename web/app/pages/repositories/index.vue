<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { RepositoryRow } from '~/utils/view-models'
import { AdminRepositoryForm, TypedConfirmModal } from '#components'

definePageMeta({ layout: 'dashboard' })

const relic = useRelic()
const toast = useToast()
const overlay = useOverlay()
const { me } = useSession()
const createForm = overlay.create(AdminRepositoryForm)
const confirmDelete = overlay.create(TypedConfirmModal)
const { data: repos, status, refresh, error } = await useAsyncData('repositories', () => listRepoRows())

async function create() {
  if (await createForm.open({ repositories: repos.value ?? [] }).result) {
    await refresh()
  }
}

async function edit(row: RepositoryRow) {
  try {
    const { repository } = await relic.repositories.getRepository({ name: row.name })
    if (await createForm.open({ repositories: repos.value ?? [], repository }).result) {
      await refresh()
    }
  } catch (err) {
    toast.add({ title: `Could not open ${row.name}`, description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function remove(repo: RepositoryRow) {
  const ok = await confirmDelete.open({
    title: `Delete repository ${repo.name}?`,
    description: 'Its artifacts are deleted too. This cannot be undone.',
    name: repo.name
  }).result
  if (!ok) {
    return
  }
  try {
    await relic.repositories.deleteRepository({ name: repo.name })
    toast.add({ title: `Repository ${repo.name} deleted`, color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the repository', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const search = ref('')
const classFilter = ref<'all' | RepositoryRow['class']>('all')
const classes = [
  { label: 'All classes', value: 'all' },
  { label: 'Local', value: 'local', icon: repositoryClassIcon('local') },
  { label: 'Remote', value: 'remote', icon: repositoryClassIcon('remote') },
  { label: 'Virtual', value: 'virtual', icon: repositoryClassIcon('virtual') }
]

const typeFilter = ref('all')
const types = computed(() => [
  { label: 'All package types', value: 'all' },
  ...[...new Set((repos.value ?? []).map(r => r.format))].sort().map(t => ({ label: t, value: t, icon: formatIcon(t) }))
])

const page = ref(1)
const pageSize = ref(50)
watch([search, classFilter, typeFilter], () => {
  page.value = 1
})
const all = computed<RepositoryRow[]>(() => repos.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(r =>
    (classFilter.value === 'all' || r.class === classFilter.value)
    && (typeFilter.value === 'all' || r.format === typeFilter.value)
    && (!q || r.name.toLowerCase().includes(q) || r.description.toLowerCase().includes(q) || r.url.toLowerCase().includes(q)))
})
const active = computed(() => [search.value.trim() !== '', classFilter.value !== 'all', typeFilter.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  classFilter.value = 'all'
  typeFilter.value = 'all'
}

const classColor = { local: 'primary', remote: 'info', virtual: 'success' } as const

const columns: TableColumn<RepositoryRow>[] = [
  { accessorKey: 'key', header: 'Key' },
  { accessorKey: 'class', header: 'Class' },
  { accessorKey: 'format', header: 'Format' },
  { accessorKey: 'url', header: 'Upstream' },
  { id: 'actions' }
]
</script>

<template>
  <UDashboardPanel id="repositories" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Repositories">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="repos" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'repository') }}</span>
        </template>
        <template v-if="me?.admin" #right>
          <UButton icon="i-lucide-plus" label="New repository" @click="create()" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters
        :active="active"
        :matched="rows.length"
        :total="all.length"
        @clear="clearFilters"
      >
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="Key, description or upstream…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="classFilter"
          aria-label="Filter by class"
          :items="classes"
          size="sm"
          class="w-40"
        />
        <USelect
          v-model="typeFilter"
          aria-label="Filter by package type"
          :items="types"
          size="sm"
          class="w-44"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="repositories"
        class="m-4"
        @retry="refresh()"
      />
      <UTable
        :data="paginate(rows, page, pageSize)"
        :columns="columns"
        :loading="status === 'pending'"
        sticky
        :ui="denseTable"
      >
        <template #key-cell="{ row }">
          <ULink :to="`/repositories/${row.original.name}`" class="inline-flex items-center gap-2 font-medium text-highlighted">
            <UIcon :name="formatIcon(row.original.format)" class="size-4 shrink-0" />
            {{ row.original.name }}
          </ULink>
        </template>
        <template #format-cell="{ row }">
          <span class="inline-flex items-center gap-1.5 text-default">
            <UIcon :name="formatIcon(row.original.format)" class="size-4 shrink-0" />
            {{ row.original.format }}
          </span>
        </template>
        <template #class-cell="{ row }">
          <UBadge
            :color="classColor[row.original.class]"
            variant="subtle"
            :icon="repositoryClassIcon(row.original.class)"
            :label="row.original.class"
            class="capitalize"
          />
        </template>
        <template #url-cell="{ row }">
          <span class="text-muted">{{ row.original.url }}</span>
        </template>
        <template #actions-cell="{ row }">
          <div v-if="me?.admin" class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-pencil"
              color="neutral"
              variant="ghost"
              :aria-label="`Edit ${row.original.name}`"
              @click="edit(row.original)"
            />
            <UButton
              icon="i-lucide-trash"
              color="error"
              variant="ghost"
              :aria-label="`Delete ${row.original.name}`"
              @click="remove(row.original)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-boxes"
            :title="active ? 'No repository matches these filters' : 'No repositories'"
            :description="active ? undefined : 'Create a repository to get started.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="repositories"
      />
    </template>
  </UDashboardPanel>
</template>
