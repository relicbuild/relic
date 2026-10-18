<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { WatchScopeKind, type Watch } from '~/gen/relic/v1/scan_pb'
import { ConfirmModal, SecurityWatchForm } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { scans } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(SecurityWatchForm)
const confirm = overlay.create(ConfirmModal)

const { data: watches, status, refresh, error } = await useAsyncData('security:watches', async () => (await scans.listWatches({})).watches)

async function edit(watch?: Watch) {
  if (await form.open({ watch }).result) {
    await refresh()
  }
}

async function remove(watch: Watch) {
  if (!(await confirm.open({ title: `Delete watch ${watch.name}?` }).result)) {
    return
  }
  try {
    await scans.deleteWatch({ name: watch.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the watch', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

function resources(w: Watch): string {
  return w.scopes.flatMap((s) => {
    const kind = enumName(WatchScopeKind, s.kind)
    return [...(s.all ? [`all ${kind === 'build' ? 'builds' : 'repositories'}`] : []), ...s.names.map(n => `${kind} ${n}`)]
  }).join(', ')
}

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const state = ref('all')
const policy = ref('all')
watch([search, state, policy], () => paging())
const all = computed<Watch[]>(() => watches.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(w =>
    (!q || w.name.toLowerCase().includes(q) || w.description.toLowerCase().includes(q))
    && (state.value === 'all' || w.active === (state.value === 'active'))
    && (policy.value === 'all' || w.policies.includes(policy.value)))
})
const active = computed(() => [search.value.trim() !== '', state.value !== 'all', policy.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  state.value = policy.value = 'all'
}
const stateItems = [
  { label: 'Active or not', value: 'all' },
  { label: 'Active', value: 'active' },
  { label: 'Inactive', value: 'inactive' }
]
const policyItems = computed(() => [
  { label: 'Any policy', value: 'all' },
  ...[...new Set(all.value.flatMap(w => w.policies))].sort().map(p => ({ label: p, value: p }))
])
function paging() {
  page.value = 1
}

const columns: TableColumn<Watch>[] = [
  { id: 'name', header: 'Watch' },
  { id: 'active', header: 'Status' },
  { id: 'resources', header: 'Covers' },
  { id: 'policies', header: 'Policies' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="security-watches" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Watches">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="watches" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'watch') }}</span>
        </template>
        <template #right>
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New watch"
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
          v-model="state"
          aria-label="Filter by state"
          :items="stateItems"
          size="sm"
          class="w-36"
        />
        <USelectMenu
          v-model="policy"
          :items="policyItems"
          value-key="value"
          icon="i-lucide-scroll-text"
          size="sm"
          class="w-44"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="watches"
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
        </template>
        <template #active-cell="{ row }">
          <UBadge
            :color="row.original.active ? 'success' : 'neutral'"
            variant="subtle"
            :label="row.original.active ? 'Active' : 'Inactive'"
          />
        </template>
        <template #resources-cell="{ row }">
          {{ resources(row.original) }}
        </template>
        <template #policies-cell="{ row }">
          {{ row.original.policies.join(', ') }}
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
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
            icon="i-lucide-eye"
            :title="active ? 'No watch matches these filters' : 'No watches'"
            :description="active ? undefined : 'Watches apply policies to repositories and builds.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="watches"
      />
    </template>
  </UDashboardPanel>
</template>
