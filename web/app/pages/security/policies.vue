<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { PolicyKind, type Policy } from '~/gen/relic/v1/scan_pb'
import { ConfirmModal, SecurityPolicyForm } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { scans } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(SecurityPolicyForm)
const confirm = overlay.create(ConfirmModal)

const { data: policies, status, refresh, error } = await useAsyncData('security:policies', async () => (await scans.listPolicies({})).policies)

async function edit(policy?: Policy) {
  if (await form.open({ policy }).result) {
    await refresh()
  }
}

async function remove(policy: Policy) {
  const ok = await confirm.open({ title: `Delete policy ${policy.name}?`, description: 'Watches must not use it.' }).result
  if (!ok) {
    return
  }
  try {
    await scans.deletePolicy({ name: policy.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the policy', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

function summary(p: Policy): string {
  return p.rules.map((r) => {
    const severity = r.minSeverity ? ` ${severityLabel(severityName(r.minSeverity))}+` : ''
    const parts = [r.malicious
      ? 'malicious packages'
      : r.secrets
        ? 'secrets'
        : r.liveSecrets
          ? 'live secrets'
          : r.misconfigurations
            ? `misconfigurations${severity}`
            : r.endOfLife
              ? `end of life${severity}`
              : r.unmaintained
                ? 'unmaintained packages'
                : r.minSeverity
                  ? severity.trim()
                  : r.cvssFrom !== undefined
                    ? `CVSS ${r.cvssFrom}–${r.cvssTo}`
                    : r.bannedLicenses.length
                      ? `bans ${r.bannedLicenses.join(', ')}`
                      : r.allowedLicenses.length ? `allows only ${r.allowedLicenses.join(', ')}` : 'a condition']
    if (r.failBuild) {
      parts.push('fails builds')
    }
    if (r.blockDownload) {
      parts.push('blocks downloads')
    }
    if (r.blockUnscanned) {
      parts.push('blocks unscanned downloads')
    }
    return `${r.name}: ${parts.join(', ')}`
  }).join('; ')
}

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const type = ref('all')
const action = ref('all')
watch([search, type, action], () => paging())
const all = computed<Policy[]>(() => policies.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(p =>
    (!q || p.name.toLowerCase().includes(q) || p.description.toLowerCase().includes(q))
    && (type.value === 'all' || enumName(PolicyKind, p.kind) === type.value)
    && (action.value === 'all' || p.rules.some(r => action.value === 'fail' ? r.failBuild : r.blockDownload)))
})
const active = computed(() => [search.value.trim() !== '', type.value !== 'all', action.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  type.value = action.value = 'all'
}
const typeItems = [
  { label: 'All types', value: 'all' },
  { label: 'Security', value: 'security' },
  { label: 'License', value: 'license' }
]
const actionItems = [
  { label: 'Any action', value: 'all' },
  { label: 'Fails builds', value: 'fail' },
  { label: 'Blocks downloads', value: 'block' }
]
function paging() {
  page.value = 1
}

const columns: TableColumn<Policy>[] = [
  { accessorKey: 'name', header: 'Policy' },
  { id: 'kind', header: 'Kind' },
  { id: 'rules', header: 'Rules' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="security-policies" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Policies">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="policies" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'policy') }}</span>
        </template>
        <template #right>
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New policy"
            @click="edit()"
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
          placeholder="Name or description…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="type"
          aria-label="Filter by type"
          :items="typeItems"
          size="sm"
          class="w-36"
        />
        <USelect
          v-model="action"
          aria-label="Filter by action"
          :items="actionItems"
          size="sm"
          class="w-44"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="policies"
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
          <span class="font-medium text-highlighted">{{ row.original.name }}</span>
          <div v-if="row.original.description" class="text-xs text-muted">
            {{ row.original.description }}
          </div>
        </template>
        <template #kind-cell="{ row }">
          <UBadge
            variant="subtle"
            color="neutral"
            :label="enumName(PolicyKind, row.original.kind)"
            class="capitalize"
          />
        </template>
        <template #rules-cell="{ row }">
          <span class="text-sm">{{ summary(row.original) }}</span>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              v-if="row.original.rules.length === 1 && row.original.rules[0]?.cvssFrom === undefined"
              icon="i-lucide-pencil"
              color="neutral"
              variant="ghost"
              aria-label="Edit"
              @click="edit(row.original)"
            />
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
            icon="i-lucide-scroll-text"
            :title="active ? 'No policy matches these filters' : 'No policies'"
            :description="active ? undefined : 'Policies decide which findings are violations.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="policies"
      />
    </template>
  </UDashboardPanel>
</template>
