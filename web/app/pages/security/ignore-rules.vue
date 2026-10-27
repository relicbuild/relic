<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { VexFormat, type IgnoreRule } from '~/gen/relic/v1/scan_pb'
import { ConfirmModal, SecurityIgnoreRuleForm } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { scans } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(SecurityIgnoreRuleForm)
const confirm = overlay.create(ConfirmModal)

const { data: rules, status, refresh, error } = await useAsyncData('security:ignore-rules', async () => (await scans.listIgnoreRules({})).rules)

async function create() {
  if (await form.open().result) {
    await refresh()
  }
}

async function remove(rule: IgnoreRule) {
  if (!rule.id || !(await confirm.open({ title: 'Delete this ignore rule?', description: rule.notes }).result)) {
    return
  }
  try {
    await scans.deleteIgnoreRule({ id: rule.id })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the ignore rule', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function exportVex(format: VexFormat, includeUnreachable: boolean) {
  try {
    const r = await scans.exportVex({ format, includeUnreachable })
    const url = URL.createObjectURL(new Blob([r.document], { type: 'application/json' }))
    const a = document.createElement('a')
    a.href = url
    a.download = format === VexFormat.CYCLONEDX ? 'relic.cdx.vex.json' : 'relic.vex.json'
    a.click()
    URL.revokeObjectURL(url)
    if (r.skipped) {
      toast.add({ title: `${pluralize(r.skipped, 'rule')} left out`, description: 'Without both a vulnerability and a package, limited in scope, or expiring: VEX cannot say those.', color: 'warning' })
    }
  } catch (err) {
    toast.add({ title: 'Could not export VEX', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const vexExports = [[
  { label: 'OpenVEX', icon: 'i-lucide-file-json', onSelect: () => exportVex(VexFormat.OPENVEX, false) },
  { label: 'CycloneDX', icon: 'i-lucide-file-json', onSelect: () => exportVex(VexFormat.CYCLONEDX, false) }
], [
  { type: 'label' as const, label: 'With the Go vulnerabilities no code calls' },
  { label: 'OpenVEX', icon: 'i-lucide-route-off', onSelect: () => exportVex(VexFormat.OPENVEX, true) },
  { label: 'CycloneDX', icon: 'i-lucide-route-off', onSelect: () => exportVex(VexFormat.CYCLONEDX, true) }
]]

const vexFile = ref<File | null>(null)
const importing = ref(false)
watch(vexFile, async (file) => {
  if (!file) {
    return
  }
  importing.value = true
  try {
    const r = await scans.importVex({ document: await file.text() })
    toast.add({
      title: `${pluralize(r.rules.length, 'ignore rule')} created from ${file.name}`,
      description: [
        r.existing ? `${pluralize(r.existing, 'statement')} already stored as ignore rules.` : '',
        r.skipped ? `${pluralize(r.skipped, 'statement')} left out: affected, under investigation, or naming no package.` : ''
      ].filter(Boolean).join(' ') || undefined,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not import VEX', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    importing.value = false
    vexFile.value = null
  }
})

function filters(r: IgnoreRule): string {
  const parts: string[] = []
  if (r.cves.length) {
    parts.push(r.cves.join(', '))
  }
  if (r.vulnerabilities.length) {
    parts.push(r.vulnerabilities.join(', '))
  }
  parts.push(...r.packages)
  if (r.watches.length) {
    parts.push(`watches ${r.watches.join(', ')}`)
  }
  return parts.join(' · ')
}

const expired = (r: IgnoreRule) => !!r.expires && new Date(toIsoDate(r.expires)) < new Date()

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const state = ref('all')
watch([search, state], () => paging())
const all = computed<IgnoreRule[]>(() => rules.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(r =>
    (!q || filters(r).toLowerCase().includes(q) || r.notes.toLowerCase().includes(q))
    && (state.value === 'all' || expired(r) === (state.value === 'expired')))
})
const active = computed(() => [search.value.trim() !== '', state.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  state.value = 'all'
}
const stateItems = [
  { label: 'Active or expired', value: 'all' },
  { label: 'Active', value: 'active' },
  { label: 'Expired', value: 'expired' }
]
function paging() {
  page.value = 1
}

const columns: TableColumn<IgnoreRule>[] = [
  { id: 'filters', header: 'Ignores' },
  { accessorKey: 'notes', header: 'Reason' },
  { id: 'expires', header: 'Expires' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="security-ignore-rules" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Ignore rules">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="rules" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'rule') }}</span>
        </template>
        <template #right>
          <UFileUpload
            v-if="administers"
            v-slot="{ open }"
            v-model="vexFile"
            :preview="false"
            :disabled="importing"
            accept=".json,application/json"
          >
            <UButton
              icon="i-lucide-file-input"
              color="neutral"
              variant="outline"
              label="Import VEX"
              aria-label="Import VEX"
              :ui="iconOnlyOnPhones"
              :loading="importing"
              @click="open()"
            />
          </UFileUpload>
          <UDropdownMenu :items="vexExports">
            <UButton
              icon="i-lucide-file-output"
              trailing-icon="i-lucide-chevron-down"
              color="neutral"
              variant="outline"
              label="Export VEX"
              aria-label="Export VEX"
              :ui="iconOnlyOnPhones"
            />
          </UDropdownMenu>
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New ignore rule"
            aria-label="New ignore rule"
            :ui="iconOnlyOnPhones"
            @click="create()"
          />
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
          placeholder="CVE, package or reason…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="state"
          aria-label="Filter by state"
          :items="stateItems"
          size="sm"
          class="w-44"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="ignore rules"
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
        <template #filters-cell="{ row }">
          <span class="font-medium text-highlighted">{{ filters(row.original) }}</span>
        </template>
        <template #expires-cell="{ row }">
          <UBadge
            v-if="expired(row.original)"
            color="neutral"
            variant="subtle"
            label="Expired"
          />
          <span v-else>{{ row.original.expires ? formatDate(toIsoDate(row.original.expires)) : 'Never' }}</span>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end">
            <UButton
              icon="i-lucide-trash"
              color="error"
              variant="ghost"
              aria-label="Delete"
              @click="remove(row.original)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-eye-off"
            :title="active ? 'No ignore rule matches these filters' : 'No ignore rules'"
            :description="active ? undefined : 'Ignore rules record accepted risks.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="rules"
      />
    </template>
  </UDashboardPanel>
</template>
