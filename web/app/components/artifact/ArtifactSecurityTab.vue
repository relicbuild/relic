<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { ComponentView, FindingView, OperationalRiskView, ScanIssueView, ScanView, Severity } from '~/utils/view-models'
import { ArtifactFindingDetailModal } from '#components'

const props = defineProps<{
  scan: ScanView
  components?: ComponentView[]
}>()
const overlay = useOverlay()
const detail = overlay.create(ArtifactFindingDetailModal)

const severity = ref<Severity | 'all'>('all')
const fixable = ref(false)
const search = ref('')

const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return props.scan.findings.filter(f =>
    (severity.value === 'all' || f.severity === severity.value)
    && (!fixable.value || f.fixedVersions.length > 0)
    && (!q || [f.id, f.package, ...f.cves].some(v => v.toLowerCase().includes(q)))
  )
})

const urgent = computed(() => (props.scan.counts.critical ?? 0) + (props.scan.counts.high ?? 0))
const fixableCount = computed(() => props.scan.findings.filter(f => f.fixedVersions.length).length)
function top(values: string[], n = 3) {
  const counts = new Map<string, number>()
  for (const v of values) {
    counts.set(v, (counts.get(v) ?? 0) + 1)
  }
  return [...counts].sort((a, b) => b[1] - a[1]).slice(0, n)
}
const topPackages = computed(() => top(props.scan.findings.map(f => `${f.package} ${f.version}`)))
const topLicenses = computed(() => top((props.components ?? []).flatMap(c => c.licenses)))

function locationsOf(f: FindingView) {
  const c = props.components?.find(c => (c.purl && c.purl === f.purl) || (c.name === f.package && c.version === f.version))
  return c?.locations
}

const score = (f: FindingView) => f.cvss.length ? Math.max(...f.cvss.map(c => c.score)) : undefined

const status = computed(() => {
  const s = props.scan
  switch (s.status) {
    case 'done':
      return { color: 'success' as const, title: `Scanned: ${pluralize(s.components, 'component')}, ${pluralize(s.findings.length, 'vulnerability')}` }
    case 'partial':
      return { color: 'warning' as const, title: `Partly scanned: ${s.reason}` }
    case 'queued':
      return { color: 'info' as const, title: 'Waiting to be scanned' }
    case 'unavailable':
      return { color: 'warning' as const, title: 'Not scanned: Relic Scan is off, since no worker with Syft and Grype reported lately' }
    case 'failed':
      return { color: 'error' as const, title: `Scan failed: ${s.reason}` }
    case 'skipped':
      return { color: 'neutral' as const, title: `Not scanned: ${s.reason}` }
    default:
      return { color: 'neutral' as const, title: 'Not a scannable artifact' }
  }
})

const analyzers: Record<ScanIssueView['analyzer'], { icon: string, label: string }> = {
  secrets: { icon: 'i-lucide-key-round', label: 'Secret' },
  misconfiguration: { icon: 'i-lucide-settings-2', label: 'Misconfiguration' },
  license: { icon: 'i-lucide-scale', label: 'License' },
  model: { icon: 'i-lucide-brain', label: 'Model' }
}

const riskColumns: TableColumn<OperationalRiskView>[] = [
  { accessorKey: 'severity', header: 'Severity' },
  { accessorKey: 'kind', header: 'Kind' },
  { accessorKey: 'package', header: 'Component' },
  { accessorKey: 'detail', header: 'Risk' }
]

const issueColumns: TableColumn<ScanIssueView>[] = [
  { accessorKey: 'severity', header: 'Severity' },
  { accessorKey: 'analyzer', header: 'Kind' },
  { accessorKey: 'title', header: 'Issue' },
  { accessorKey: 'file', header: 'Where' }
]

const columns: TableColumn<FindingView>[] = [
  { accessorKey: 'severity', header: 'Severity' },
  { accessorKey: 'id', header: 'Vulnerability' },
  { id: 'cvss', header: 'CVSS' },
  { accessorKey: 'package', header: 'Package' },
  { accessorKey: 'fixedVersions', header: 'Fixed in' },
  { accessorKey: 'summary', header: 'Summary' }
]
</script>

<template>
  <div class="flex flex-col gap-4 py-4">
    <UAlert
      :color="status.color"
      variant="subtle"
      icon="i-lucide-scan-search"
      :title="status.title"
      :description="[scan.source && `Scan of the image manifest (${scan.source}).`, scan.vulnerabilityDatabaseBuilt && `Vulnerability database built ${formatDate(scan.vulnerabilityDatabaseBuilt)}.`].filter(Boolean).join(' ') || undefined"
    />

    <template v-if="scan.findings.length">
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <UCard :ui="{ body: 'p-3 sm:p-3' }">
          <p class="text-xs text-muted">
            Critical and high
          </p>
          <p class="text-2xl font-semibold" :class="urgent ? 'text-error' : 'text-highlighted'">
            {{ urgent }}
          </p>
        </UCard>
        <UCard :ui="{ body: 'p-3 sm:p-3' }">
          <p class="text-xs text-muted">
            With a fix available
          </p>
          <p class="text-2xl font-semibold text-highlighted">
            {{ fixableCount }} <span class="text-sm font-normal text-muted">of {{ scan.findings.length }}</span>
          </p>
        </UCard>
        <UCard :ui="{ body: 'p-3 sm:p-3' }">
          <p class="mb-1 text-xs text-muted">
            Most affected packages
          </p>
          <p v-for="[name, n] in topPackages" :key="name" class="flex justify-between gap-2 text-sm">
            <span class="truncate text-default">{{ name }}</span><span class="text-muted">{{ n }}</span>
          </p>
        </UCard>
        <UCard :ui="{ body: 'p-3 sm:p-3' }">
          <p class="mb-1 text-xs text-muted">
            Top licenses
          </p>
          <p v-for="[name, n] in topLicenses" :key="name" class="flex justify-between gap-2 text-sm">
            <span class="truncate text-default">{{ name }}</span><span class="text-muted">{{ n }}</span>
          </p>
          <p v-if="!topLicenses.length" class="text-sm text-muted">
            {{ components ? 'None recorded' : 'Loading…' }}
          </p>
        </UCard>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <UButton
          v-for="s in severities.filter(s => scan.counts[s])"
          :key="s"
          :color="severityColor(s)"
          :variant="severity === s ? 'solid' : 'subtle'"
          size="sm"
          :label="`${s} ${scan.counts[s]}`"
          @click="severity = severity === s ? 'all' : s"
        />
        <USwitch v-model="fixable" label="Has a fix" class="ms-2" />
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="CVE or package"
          size="sm"
          class="ms-auto w-56"
        />
      </div>

      <UTable
        :data="rows"
        :columns="columns"
        class="cursor-pointer"
        @select="(_e, row) => detail.open({ finding: row.original, locations: locationsOf(row.original) })"
      >
        <template #severity-cell="{ row }">
          <UBadge :color="severityColor(row.original.severity)" variant="subtle" :label="severityLabel(row.original.severity)" />
        </template>
        <template #id-cell="{ row }">
          <span class="flex items-center gap-2">
            <span class="font-medium text-highlighted">{{ row.original.id }}</span>
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
          <div v-if="row.original.cves.length && row.original.cves[0] !== row.original.id" class="text-xs text-muted">
            {{ row.original.cves.join(', ') }}
          </div>
        </template>
        <template #cvss-cell="{ row }">
          {{ score(row.original)?.toFixed(1) ?? '—' }}
        </template>
        <template #package-cell="{ row }">
          {{ row.original.package }} <span class="text-muted">{{ row.original.version }}</span>
        </template>
        <template #fixedVersions-cell="{ row }">
          {{ row.original.fixedVersions.join(', ') || '—' }}
        </template>
        <template #summary-cell="{ row }">
          <span class="line-clamp-2 max-w-md whitespace-normal">{{ row.original.summary }}</span>
        </template>
        <template #empty>
          <UEmpty icon="i-lucide-filter-x" title="No vulnerabilities match the filters" />
        </template>
      </UTable>
    </template>
    <template v-if="scan.issues.length">
      <h3 class="mt-2 text-sm font-semibold text-highlighted">
        Secrets, misconfigurations, licenses and model code in files
      </h3>
      <UTable :data="scan.issues" :columns="issueColumns">
        <template #severity-cell="{ row }">
          <UBadge :color="severityColor(row.original.severity)" variant="subtle" :label="severityLabel(row.original.severity)" />
        </template>
        <template #analyzer-cell="{ row }">
          <UBadge
            color="neutral"
            variant="outline"
            :icon="analyzers[row.original.analyzer].icon"
            :label="analyzers[row.original.analyzer].label"
          />
        </template>
        <template #title-cell="{ row }">
          <span class="line-clamp-2 max-w-md whitespace-normal">{{ row.original.title }}</span>
          <div class="flex items-center gap-2 text-xs text-muted">
            {{ row.original.rule }}
            <UBadge
              v-if="row.original.validation"
              :color="secretValidations[row.original.validation].color"
              variant="subtle"
              size="sm"
              :label="secretValidations[row.original.validation].label"
            />
          </div>
        </template>
        <template #file-cell="{ row }">
          <code class="text-xs">{{ row.original.file }}{{ row.original.line ? `:${row.original.line}` : '' }}</code>
          <div v-if="row.original.resource" class="text-xs text-muted">
            {{ row.original.resource }}
          </div>
        </template>
      </UTable>
    </template>
    <template v-if="scan.risks.length">
      <h3 class="mt-2 text-sm font-semibold text-highlighted">
        Operational risks: end of life, deprecated and unmaintained components
      </h3>
      <UTable :data="scan.risks" :columns="riskColumns">
        <template #severity-cell="{ row }">
          <UBadge :color="severityColor(row.original.severity)" variant="subtle" :label="severityLabel(row.original.severity)" />
        </template>
        <template #kind-cell="{ row }">
          <UBadge
            color="neutral"
            variant="outline"
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
      </UTable>
    </template>
  </div>
</template>
