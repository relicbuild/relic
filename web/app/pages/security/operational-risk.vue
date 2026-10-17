<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { OperationalRiskKind, OperationalRiskView } from '~/utils/view-models'
import { operationalRiskView } from '~/utils/mappers'

definePageMeta({ layout: 'dashboard' })

const { scans } = useRelic()
const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const query = useDebounced(search)
const kind = ref<OperationalRiskKind | 'all'>('all')
const repository = ref('all')

const filters = [kind, repository, query]
watch(filters, () => {
  page.value = 1
})
const active = computed(() =>
  [search.value.trim() !== '', kind.value !== 'all', repository.value !== 'all'].filter(Boolean).length
)
function clearFilters() {
  search.value = ''
  kind.value = 'all'
  repository.value = 'all'
}

const any = (v: string) => (v === 'all' ? '' : v)
const { data, status, error, refresh } = await useAsyncData(
  'security:operational-risk',
  async () => {
    const r = await scans.listOperationalRisks({
      page: page.value, pageSize: pageSize.value, query: query.value, kind: any(kind.value), repository: any(repository.value)
    })
    return { items: r.risks.map(operationalRiskView), total: toNumber(r.total) }
  },
  { watch: [page, pageSize, ...filters] }
)

const { data: repos } = await useAsyncData('security:operational-risk:repos', listRepoRows)
const { data: source } = await useAsyncData('security:operational-risk:source', () => scans.getEndOfLifeStatus({}))

const kindItems = [
  { label: 'All kinds', value: 'all' },
  ...Object.entries(operationalRiskKinds).map(([value, k]) => ({ label: k.label, value, icon: k.icon }))
]
const repositoryItems = computed(() => [
  { label: 'All repositories', value: 'all' },
  ...(repos.value ?? []).filter(r => r.class !== 'virtual').map(r => ({ label: r.name, value: r.name }))
])

const columns: TableColumn<OperationalRiskView>[] = [
  { accessorKey: 'severity', header: 'Severity' },
  { accessorKey: 'kind', header: 'Kind' },
  { accessorKey: 'package', header: 'Component' },
  { accessorKey: 'detail', header: 'Risk' },
  { accessorKey: 'path', header: 'Artifact' }
]
</script>

<template>
  <UDashboardPanel id="security-operational-risk" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Operational risk">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(data.total, 'risk') }}</span>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters :active="active" :matched="data?.total" @clear="clearFilters">
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="Package or product…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="kind"
          aria-label="Filter by kind"
          :items="kindItems"
          size="sm"
          class="w-40"
        />
        <USelectMenu
          v-model="repository"
          :items="repositoryItems"
          value-key="value"
          icon="i-lucide-box"
          size="sm"
          class="w-48"
        />
        <span v-if="source" class="ms-auto text-xs text-muted">
          <template v-if="source.source">
            End-of-life data: {{ source.products }} products, changed {{ formatDate(toIsoDate(source.changed)) }}
          </template>
          <template v-else>No end-of-life data yet</template>
        </span>
      </TableFilters>

      <LoadError
        :error="error"
        what="operational risks"
        class="m-4"
        @retry="refresh()"
      />
      <UTable
        :data="data?.items ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        sticky
        :ui="denseTable"
      >
        <template #severity-cell="{ row }">
          <UBadge
            :color="severityColor(row.original.severity)"
            variant="subtle"
            size="sm"
            :label="row.original.severity"
          />
        </template>
        <template #kind-cell="{ row }">
          <UBadge
            color="neutral"
            variant="outline"
            size="sm"
            :icon="operationalRiskKinds[row.original.kind].icon"
            :label="operationalRiskKinds[row.original.kind].label"
          />
        </template>
        <template #package-cell="{ row }">
          {{ row.original.package }} <span class="text-muted">{{ row.original.version }}</span>
        </template>
        <template #detail-cell="{ row }">
          <span class="line-clamp-2 max-w-md whitespace-normal">{{ row.original.detail }}</span>
        </template>
        <template #path-cell="{ row }">
          <ULink :to="{ path: `/repositories/${row.original.repo}`, query: { path: row.original.path } }" class="break-all text-xs">
            {{ row.original.repo }}/{{ row.original.path }}
          </ULink>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-calendar-check"
            :title="active ? 'No risk matches these filters' : 'No operational risks'"
            :description="active ? undefined : 'No scanned component is past or near end of life, deprecated or unmaintained.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="data?.total ?? 0"
        noun="risks"
      />
    </template>
  </UDashboardPanel>
</template>
