<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { ReleaseView } from '~/utils/view-models'

definePageMeta({ layout: 'dashboard' })

const { releases } = useRelic()
const { data, status, error, refresh } = await useAsyncData('releases', async () =>
  (await releases.listReleases({})).releases.map(releaseView))

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
watch(search, () => {
  page.value = 1
})
const all = computed<ReleaseView[]>(() => data.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(r => !q || r.name.toLowerCase().includes(q) || r.project.toLowerCase().includes(q))
})
const active = computed(() => (search.value.trim() !== '' ? 1 : 0))

const releaseLink = (r: ReleaseView) => ({ path: `/releases/${encodeURIComponent(r.name)}`, query: r.project ? { project: r.project } : {} })

const columns: TableColumn<ReleaseView>[] = [
  { accessorKey: 'name', header: 'Release' },
  { accessorKey: 'version', header: 'Latest version' },
  { accessorKey: 'versions', header: 'Versions' },
  { accessorKey: 'created', header: 'Created' }
]
</script>

<template>
  <UDashboardPanel id="releases" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Releases">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'release') }}</span>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters
        :active="active"
        :matched="rows.length"
        :total="all.length"
        @clear="search = ''"
      >
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="Release or project…"
          size="sm"
          class="w-full sm:w-64"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="releases"
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
        <template #name-cell="{ row }">
          <ULink :to="releaseLink(row.original)" class="font-medium text-highlighted">
            {{ row.original.name }}
          </ULink>
          <UBadge
            v-if="row.original.project"
            class="ms-2"
            color="neutral"
            variant="subtle"
            :label="row.original.project"
          />
        </template>
        <template #version-cell="{ row }">
          <span class="font-mono">{{ row.original.version }}</span>
        </template>
        <template #versions-cell="{ row }">
          <span class="tabular-nums">{{ row.original.versions }}</span>
        </template>
        <template #created-cell="{ row }">
          {{ formatDate(row.original.created) }}
        </template>
        <template #empty>
          <UEmpty
            v-if="!error"
            variant="naked"
            icon="i-lucide-package-check"
            :title="active ? 'No release matches the search' : 'No releases'"
            :description="active ? undefined : 'Create a release with relic release create, then promote it here.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="releases"
      />
    </template>
  </UDashboardPanel>
</template>
