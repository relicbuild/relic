<script setup lang="ts">
import type { DropdownMenuItem, TableColumn } from '@nuxt/ui'
import type { CleanupPolicyView } from '~/utils/view-models'
import { cleanupCriteria, cleanupPolicyView } from '~/utils/mappers'
import { AdminCleanupPolicyForm, AdminCleanupPreview, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { cleanup } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(AdminCleanupPolicyForm)
const preview = overlay.create(AdminCleanupPreview)
const confirm = overlay.create(ConfirmModal)

const { data, status, refresh, error } = await useAsyncData('admin:cleanup', async () => {
  const [policies, repositories] = await Promise.all([
    cleanup.listCleanupPolicies({}).then(r => r.policies.map(cleanupPolicyView)),
    listRepoRows()
  ])
  return { policies, repositories: repositories.filter(r => r.class === 'local') }
})

async function edit(policy?: CleanupPolicyView) {
  if (await form.open({ policy, repositories: data.value?.repositories ?? [] }).result) {
    await refresh()
  }
}

async function run(policy: CleanupPolicyView) {
  if (!(await confirm.open({ title: `Run ${policy.name} now?`, description: 'What the policy selects moves to the trash.', confirmLabel: 'Run now' }).result)) {
    return
  }
  try {
    const { moved } = await cleanup.runCleanupPolicy({ name: policy.name })
    toast.add({ title: `Moved ${pluralize(toNumber(moved), 'item')} to the trash`, color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not run the cleanup policy', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function remove(policy: CleanupPolicyView) {
  if (!(await confirm.open({ title: `Delete the cleanup policy ${policy.name}?`, description: 'Items it moved stay in the trash.' }).result)) {
    return
  }
  try {
    await cleanup.deleteCleanupPolicy({ name: policy.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the cleanup policy', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

function actions(policy: CleanupPolicyView): DropdownMenuItem[][] {
  if (!administers.value) {
    return [[
      { label: 'Preview', icon: 'i-lucide-eye', onSelect: () => preview.open({ name: policy.name }) }
    ]]
  }
  return [[
    { label: 'Edit', icon: 'i-lucide-pencil', onSelect: () => edit(policy) },
    { label: 'Preview', icon: 'i-lucide-eye', onSelect: () => preview.open({ name: policy.name }) },
    { label: 'Run now', icon: 'i-lucide-play', onSelect: () => run(policy) }
  ], [
    { label: 'Delete', icon: 'i-lucide-trash', color: 'error', onSelect: () => remove(policy) }
  ]]
}

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const enabled = ref('all')
watch([search, enabled], () => {
  page.value = 1
})
const all = computed<CleanupPolicyView[]>(() => data.value?.policies ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(p =>
    (!q || p.name.toLowerCase().includes(q) || p.pathPattern.toLowerCase().includes(q))
    && (enabled.value === 'all' || p.enabled === (enabled.value === 'enabled')))
})
const active = computed(() => [search.value.trim() !== '', enabled.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  enabled.value = 'all'
}
const enabledItems = [
  { label: 'Enabled or not', value: 'all' },
  { label: 'Enabled', value: 'enabled' },
  { label: 'Disabled', value: 'disabled' }
]

const columns: TableColumn<CleanupPolicyView>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'repositories', header: 'Repositories' },
  { accessorKey: 'pathPattern', header: 'Pattern' },
  { id: 'criteria', header: 'Criteria' },
  { accessorKey: 'lastRun', header: 'Last run' },
  { id: 'actions' }
]
</script>

<template>
  <UDashboardPanel id="admin-cleanup" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Cleanup policies">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'policy') }}</span>
        </template>
        <template #right>
          <UButton
            icon="i-lucide-refresh-cw"
            color="neutral"
            variant="outline"
            aria-label="Refresh"
            :loading="status === 'pending'"
            @click="refresh()"
          />
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New policy"
            :disabled="!data?.repositories.length"
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
          placeholder="Name or pattern…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="enabled"
          aria-label="Filter by enabled"
          :items="enabledItems"
          size="sm"
          class="w-40"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="cleanup policies"
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
          <UBadge
            v-if="!row.original.enabled"
            class="ms-2"
            color="neutral"
            variant="subtle"
            label="Disabled"
          />
        </template>
        <template #repositories-cell="{ row }">
          <span class="text-default">{{ row.original.repositories.join(', ') }}</span>
        </template>
        <template #pathPattern-cell="{ row }">
          <span class="font-mono text-xs">{{ row.original.pathPattern || '**' }}</span>
        </template>
        <template #criteria-cell="{ row }">
          <span class="text-default">{{ cleanupCriteria(row.original) }}</span>
        </template>
        <template #lastRun-cell="{ row }">
          <UBadge
            v-if="!row.original.lastRun"
            color="neutral"
            variant="subtle"
            label="Never run"
          />
          <template v-else>
            <UTooltip :text="row.original.lastError" :disabled="!row.original.lastError">
              <UBadge
                :color="row.original.lastError ? 'error' : 'success'"
                variant="subtle"
                :label="row.original.lastError ? 'Failed' : `${pluralize(row.original.lastMoved, 'item')} moved`"
              />
            </UTooltip>
            <div class="text-xs text-muted">
              {{ formatDate(row.original.lastRun) }}
            </div>
          </template>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end">
            <UDropdownMenu :items="actions(row.original)" :content="{ align: 'end' }">
              <UButton
                icon="i-lucide-ellipsis-vertical"
                color="neutral"
                variant="ghost"
                aria-label="Actions"
              />
            </UDropdownMenu>
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-brush-cleaning"
            :title="active ? 'No cleanup policy matches these filters' : 'No cleanup policies'"
            :description="active ? undefined : 'A cleanup policy moves old or unused items of local repositories to the trash every day.'"
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
