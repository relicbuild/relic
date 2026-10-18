<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { BuildRunView } from '~/utils/view-models'

definePageMeta({ layout: 'dashboard' })

const route = useRoute()
const name = computed(() => String(route.params.name))
const project = computed(() => typeof route.query.project === 'string' ? route.query.project : '')
const relic = useRelic()
const { data: runs, status, error, refresh } = await useAsyncData(
  () => `runs:${project.value}/${name.value}`,
  async () => (await relic.builds.listBuildRuns({ project: project.value, name: name.value })).runs
    .map((r): BuildRunView => ({ number: r.number, started: toIsoDate(r.started) })),
  { watch: [project, name] }
)

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const since = ref('all')
watch([search, since, name], () => {
  page.value = 1
})
const sinceItems = [
  { label: 'Any time', value: 'all' },
  { label: 'Last 24 hours', value: '1' },
  { label: 'Last 7 days', value: '7' },
  { label: 'Last 30 days', value: '30' }
]
const recent = (when: string) => since.value === 'all' || Date.now() - new Date(when).getTime() <= Number(since.value) * 86_400_000
const all = computed<BuildRunView[]>(() => runs.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(r => (!q || r.number.toLowerCase().includes(q)) && recent(r.started))
})
const active = computed(() => [search.value.trim() !== '', since.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  since.value = 'all'
}

const columns: TableColumn<BuildRunView>[] = [
  { accessorKey: 'number', header: 'Number' },
  { accessorKey: 'started', header: 'Started' }
]
</script>

<template>
  <UDashboardPanel id="build-runs" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar :title="name">
        <template #leading>
          <UDashboardSidebarCollapse />
          <UButton
            icon="i-lucide-arrow-left"
            color="neutral"
            variant="ghost"
            :to="buildsLink(project)"
            aria-label="All builds"
          />
        </template>
        <template #trailing>
          <span v-if="runs" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'run') }}</span>
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
          placeholder="Run number…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="since"
          aria-label="Filter by start"
          :items="sinceItems"
          icon="i-lucide-calendar"
          size="sm"
          class="w-40"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="build runs"
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
        <template #number-cell="{ row }">
          <ULink :to="buildRunLink(name, row.original.number, project)" class="font-medium text-highlighted">
            {{ row.original.number }}
          </ULink>
        </template>
        <template #started-cell="{ row }">
          {{ formatDate(row.original.started) }}
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-hammer"
            :title="active ? 'No run matches these filters' : 'No runs'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="runs"
      />
    </template>
  </UDashboardPanel>
</template>
