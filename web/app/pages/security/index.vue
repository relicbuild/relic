<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { GlobalFinding } from '~/utils/view-models'
import { globalFinding } from '~/utils/mappers'
import { SecurityCveDetailSlideover } from '#components'

definePageMeta({ layout: 'dashboard' })

const { scans } = useRelic()
const cveDetail = useOverlay().create(SecurityCveDetailSlideover)
const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const query = useDebounced(search)
const severity = ref('all')
const repository = ref('all')
const packageType = ref('all')
const fixableOnly = ref(false)
const kevOnly = ref(false)
const maliciousOnly = ref(false)

const filters = [severity, repository, packageType, fixableOnly, kevOnly, maliciousOnly, query]
watch(filters, () => {
  page.value = 1
})
const active = computed(() =>
  [search.value.trim() !== '', severity.value !== 'all', repository.value !== 'all', packageType.value !== 'all', fixableOnly.value, kevOnly.value, maliciousOnly.value].filter(Boolean).length
)
function clearFilters() {
  search.value = ''
  severity.value = repository.value = packageType.value = 'all'
  fixableOnly.value = kevOnly.value = maliciousOnly.value = false
}

const any = (v: string) => (v === 'all' ? '' : v)
const { data, status, error, refresh } = await useAsyncData(
  'security:findings',
  async () => {
    const r = await scans.listFindings({
      page: page.value, pageSize: pageSize.value, query: query.value, severity: findingSeverityOf(any(severity.value)), repository: any(repository.value),
      purlType: any(packageType.value), fixableOnly: fixableOnly.value, kevOnly: kevOnly.value, maliciousOnly: maliciousOnly.value
    })
    return { items: r.findings.map(globalFinding), total: toNumber(r.total) }
  },
  { watch: [page, pageSize, ...filters] }
)

const { data: repos } = await useAsyncData('security:findings:repos', listRepoRows)

const severityItems = [
  { label: 'All severities', value: 'all' },
  ...severities.map(s => ({ label: severityLabel(s), value: s }))
]
const repositoryItems = computed(() => [
  { label: 'All repositories', value: 'all' },
  ...(repos.value ?? []).filter(r => r.class !== 'virtual').map(r => ({ label: r.name, value: r.name }))
])
const packageTypes = [
  { label: 'All package types', value: 'all' },
  ...['npm', 'maven', 'pypi', 'golang', 'nuget', 'gem', 'cargo', 'composer', 'conan', 'deb', 'apk', 'rpm', 'generic']
    .map(t => ({ label: t, value: t }))
]

const columns: TableColumn<GlobalFinding>[] = [
  { accessorKey: 'severity', header: 'Severity' },
  { accessorKey: 'id', header: 'Vulnerability' },
  { accessorKey: 'epss', header: 'EPSS' },
  { accessorKey: 'package', header: 'Package' },
  { accessorKey: 'fixedVersions', header: 'Fixed in' },
  { accessorKey: 'path', header: 'Artifact' }
]
</script>

<template>
  <UDashboardPanel id="security-findings" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Vulnerabilities">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(data.total, 'finding') }}</span>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters :active="active" :matched="data?.total" @clear="clearFilters">
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="Vulnerability, CVE or package…"
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
        <USelectMenu
          v-model="repository"
          :items="repositoryItems"
          value-key="value"
          icon="i-lucide-box"
          size="sm"
          class="w-48"
        />
        <USelect
          v-model="packageType"
          aria-label="Filter by package type"
          :items="packageTypes"
          icon="i-lucide-package"
          size="sm"
          class="w-44"
        />
        <USwitch
          v-model="fixableOnly"
          label="Fixable only"
          size="sm"
          class="whitespace-nowrap"
        />
        <USwitch
          v-model="kevOnly"
          label="Known exploited only"
          size="sm"
          class="whitespace-nowrap"
        />
        <USwitch
          v-model="maliciousOnly"
          label="Malicious only"
          size="sm"
          class="whitespace-nowrap"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="vulnerabilities"
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
            :label="severityLabel(row.original.severity)"
          />
        </template>
        <template #id-cell="{ row }">
          <div class="flex flex-col gap-0.5">
            <span class="flex items-center gap-2">
              <code class="text-xs font-semibold text-highlighted">{{ row.original.id }}</code>
              <UBadge
                v-if="row.original.kev"
                color="error"
                variant="solid"
                size="sm"
                icon="i-lucide-siren"
                label="KEV"
              />
              <UBadge
                v-if="row.original.malicious"
                color="error"
                variant="solid"
                size="sm"
                icon="i-lucide-skull"
                label="Malicious"
              />
              <UBadge
                v-if="row.original.reachability !== 'unknown'"
                :color="reachabilityBadges[row.original.reachability].color"
                variant="subtle"
                size="sm"
                :icon="reachabilityBadges[row.original.reachability].icon"
                :label="reachabilityBadges[row.original.reachability].label"
              />
            </span>
            <span v-if="row.original.cves.length" class="flex flex-wrap gap-x-2">
              <ULink
                v-for="cve in row.original.cves"
                :key="cve"
                as="button"
                class="text-xs"
                @click="cveDetail.open({ id: cve })"
              >
                {{ cve }}
              </ULink>
            </span>
          </div>
        </template>
        <template #epss-cell="{ row }">
          <UBadge
            v-if="row.original.epss !== undefined"
            :color="epssColor(row.original.epss)"
            variant="soft"
            size="sm"
            class="tabular-nums"
            :label="formatPercent(row.original.epss)"
          />
          <span v-else class="text-muted">—</span>
        </template>
        <template #package-cell="{ row }">
          {{ row.original.package }} <span class="text-muted">{{ row.original.version }}</span>
        </template>
        <template #fixedVersions-cell="{ row }">
          <span v-if="row.original.fixedVersions.length" class="flex max-w-60 flex-wrap gap-1">
            <UBadge
              v-for="v in row.original.fixedVersions"
              :key="v"
              color="success"
              variant="subtle"
              size="sm"
              class="font-mono"
              :label="v"
            />
          </span>
          <span v-else class="text-xs text-muted">no fix listed</span>
        </template>
        <template #path-cell="{ row }">
          <ULink :to="{ path: `/repositories/${row.original.repo}`, query: { path: row.original.path } }" class="break-all text-xs">
            {{ row.original.repo }}/{{ row.original.path }}
          </ULink>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-shield-check"
            :title="active ? 'No finding matches these filters' : 'No findings'"
            :description="active ? undefined : 'Nothing scanned matches, or nothing is vulnerable.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="data?.total ?? 0"
        noun="findings"
      />
    </template>
  </UDashboardPanel>
</template>
