<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { CveRow } from '~/utils/view-models'
import { cveRowView, findingSeverityOf } from '~/utils/mappers'
import { severities } from '~/utils/view-models'
import { Severity as SeverityEnum } from '~/gen/relic/v1/scan_pb'
import { SecurityCveDetailSlideover } from '#components'

definePageMeta({ layout: 'dashboard' })

const { scans } = useRelic()
const page = ref(1)
const pageSize = ref(50)
const severity = ref('all')
const sort = ref('epss')
const minEpss = ref('0')
const kevOnly = ref(false)
const search = ref('')
const query = useDebounced(search)
const detail = useOverlay().create(SecurityCveDetailSlideover)

watch([severity, sort, minEpss, kevOnly, query], () => {
  page.value = 1
})
const active = computed(() => [search.value.trim() !== '', severity.value !== 'all', minEpss.value !== '0', kevOnly.value].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  severity.value = 'all'
  minEpss.value = '0'
  kevOnly.value = false
}

const { data, status, error, refresh } = await useAsyncData(
  'security:cves',
  async () => {
    const r = await scans.listCves({
      page: page.value,
      pageSize: pageSize.value,
      query: query.value,
      sort: sort.value,
      kevOnly: kevOnly.value,
      minEpss: Number(minEpss.value),
      severity: severity.value === 'all' ? SeverityEnum.UNSPECIFIED : findingSeverityOf(severity.value)
    })
    return { items: r.cves.map(cveRowView), total: toNumber(r.total), feeds: r.feeds.map(f => ({ name: f.name, version: f.version, fetched: toIsoDate(f.lastFetchedAt) })) }
  },
  { watch: [page, pageSize, severity, sort, minEpss, kevOnly, query] }
)

const severityItems = [
  { label: 'All severities', value: 'all' },
  ...severities.filter(s => s !== 'unknown').map(s => ({ label: severityLabel(s), value: s }))
]
const sorts = [
  { label: 'Exploit risk', value: 'epss' },
  { label: 'CVSS score', value: 'cvss' },
  { label: 'Most affected', value: 'affected' },
  { label: 'Newest', value: 'published' },
  { label: 'CVE ID', value: 'id' }
]
const epssFloors = [
  { label: 'Any EPSS', value: '0' },
  { label: 'EPSS ≥ 1%', value: '0.01' },
  { label: 'EPSS ≥ 10%', value: '0.1' },
  { label: 'EPSS ≥ 50%', value: '0.5' }
]

const exploitationColor = { active: 'error', poc: 'warning', none: 'neutral' } as const

const columns: TableColumn<CveRow>[] = [
  { accessorKey: 'id', header: 'CVE' },
  { accessorKey: 'severity', header: 'Severity' },
  { accessorKey: 'cvss', header: 'CVSS' },
  { accessorKey: 'epss', header: 'EPSS' },
  { accessorKey: 'kev', header: 'Exploited' },
  { accessorKey: 'published', header: 'Published' },
  { accessorKey: 'artifacts', header: 'Affects' }
]

const feedNames: Record<string, string> = { epss: 'EPSS', kev: 'KEV' }
const feedLine = computed(() =>
  (data.value?.feeds ?? []).map(f => `${feedNames[f.name] ?? f.name} ${f.version}`).join(' · ')
)
</script>

<template>
  <UDashboardPanel id="security-cves" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="CVEs">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(data.total, 'CVE') }}</span>
        </template>
        <template #right>
          <UTooltip text="Feeds read by the worker: FIRST EPSS and CISA KEV">
            <span v-if="feedLine" class="text-xs text-muted">{{ feedLine }}</span>
          </UTooltip>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters :active="active" :matched="data?.total" @clear="clearFilters">
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="CVE ID or title…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="severity"
          aria-label="Filter by severity"
          :items="severityItems"
          size="sm"
          class="w-36"
        />
        <USelect
          v-model="minEpss"
          aria-label="Minimum EPSS"
          :items="epssFloors"
          size="sm"
          class="w-36"
        />
        <USwitch
          v-model="kevOnly"
          label="Known exploited only"
          size="sm"
          class="whitespace-nowrap"
        />
        <template #right>
          <USelect
            v-model="sort"
            aria-label="Sort by"
            :items="sorts"
            icon="i-lucide-arrow-down-wide-narrow"
            size="sm"
            class="w-44"
          />
        </template>
      </TableFilters>

      <LoadError
        :error="error"
        what="CVEs"
        class="m-4"
        @retry="refresh()"
      />
      <UTable
        :data="data?.items ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        sticky
        :ui="denseTable"
        class="cursor-pointer"
        @select="(_e: Event, row: { original: CveRow }) => detail.open({ id: row.original.id })"
      >
        <template #id-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.id }}</span>
          <div v-if="row.original.title" class="max-w-md truncate text-xs text-muted">
            {{ row.original.title }}
          </div>
        </template>
        <template #severity-cell="{ row }">
          <UBadge :color="severityColor(row.original.severity)" variant="subtle" :label="severityLabel(row.original.severity)" />
        </template>
        <template #cvss-cell="{ row }">
          <span v-if="row.original.cvss.score !== undefined" class="tabular-nums">
            {{ row.original.cvss.score.toFixed(1) }}
            <span class="text-xs text-muted">v{{ row.original.cvss.version }}</span>
          </span>
          <span v-else class="text-muted">—</span>
        </template>
        <template #epss-cell="{ row }">
          <UBadge
            v-if="row.original.epss !== undefined"
            :color="epssColor(row.original.epss)"
            variant="soft"
            class="tabular-nums"
            :label="formatPercent(row.original.epss)"
          />
          <span v-else class="text-muted">—</span>
          <div v-if="row.original.epssPercentile !== undefined" class="text-xs text-muted">
            top {{ Math.max(0.1, 100 - row.original.epssPercentile * 100).toFixed(1) }}%
          </div>
        </template>
        <template #kev-cell="{ row }">
          <div class="flex flex-wrap gap-1">
            <UBadge
              v-if="row.original.kev"
              color="error"
              variant="solid"
              icon="i-lucide-siren"
              label="KEV"
            />
            <UBadge
              v-if="row.original.ransomware"
              color="error"
              variant="outline"
              label="Ransomware"
            />
            <UBadge
              v-if="row.original.exploitation && !row.original.kev"
              :color="exploitationColor[row.original.exploitation]"
              variant="subtle"
              :label="row.original.exploitation"
            />
          </div>
        </template>
        <template #published-cell="{ row }">
          <span class="text-muted">{{ formatDay(row.original.published) || '—' }}</span>
        </template>
        <template #artifacts-cell="{ row }">
          {{ pluralize(row.original.artifacts, 'artifact') }}
          <div class="text-xs text-muted">
            {{ pluralize(row.original.packages, 'package') }}
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-shield-check"
            :title="active ? 'No CVE matches these filters' : 'No CVEs'"
            :description="active ? undefined : 'No scan finding names a CVE.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="data?.total ?? 0"
        noun="CVEs"
      />
    </template>
  </UDashboardPanel>
</template>
