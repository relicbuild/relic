<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { ReplicationView } from '~/utils/view-models'
import { replicationView } from '~/utils/mappers'
import { AdminReplicationForm, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { replications } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(AdminReplicationForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, refresh, error } = await useAsyncData('admin:replications', async () => {
  const [list, repositories] = await Promise.all([
    replications.listReplications({}).then(r => r.replications.map(replicationView)),
    listRepoRows()
  ])
  return { replications: list, repositories }
})

const available = computed(() => {
  const taken = new Set(data.value?.replications.map(r => r.repository))
  return (data.value?.repositories ?? []).filter(r => r.class === 'local' && !taken.has(r.name))
})

async function edit(replication?: ReplicationView) {
  const repositories = replication ? (data.value?.repositories ?? []).filter(r => r.name === replication.repository) : available.value
  if (await form.open({ replication, repositories }).result) {
    await refresh()
  }
}

async function run(replication: ReplicationView) {
  try {
    await replications.runReplication({ repository: replication.repository })
    toast.add({ title: `Replication of ${replication.repository} queued`, description: 'Refresh to see the result.', color: 'success', icon: 'i-lucide-check' })
  } catch (err) {
    toast.add({ title: 'Could not start the replication', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function remove(replication: ReplicationView) {
  if (!(await confirm.open({ title: `Delete the replication of ${replication.repository}?`, description: 'Content already pushed stays on the target.' }).result)) {
    return
  }
  try {
    await replications.deleteReplication({ repository: replication.repository })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the replication', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const lastRun: Record<ReplicationView['lastStatus'], { label: string, color: 'neutral' | 'success' | 'error' }> = {
  never: { label: 'Never run', color: 'neutral' },
  ok: { label: 'Succeeded', color: 'success' },
  failed: { label: 'Failed', color: 'error' }
}

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const lastStatus = ref('all')
const enabled = ref('all')
watch([search, lastStatus, enabled], () => {
  page.value = 1
})
const all = computed<ReplicationView[]>(() => data.value?.replications ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(r =>
    (!q || r.repository.toLowerCase().includes(q) || r.targetUrl.toLowerCase().includes(q))
    && (lastStatus.value === 'all' || r.lastStatus === lastStatus.value)
    && (enabled.value === 'all' || r.enabled === (enabled.value === 'enabled')))
})
const active = computed(() => [search.value.trim() !== '', lastStatus.value !== 'all', enabled.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  lastStatus.value = enabled.value = 'all'
}
const statusItems = [
  { label: 'Any last run', value: 'all' },
  { label: 'Succeeded', value: 'ok' },
  { label: 'Failed', value: 'failed' },
  { label: 'Never run', value: 'never' }
]
const enabledItems = [
  { label: 'Enabled or not', value: 'all' },
  { label: 'Enabled', value: 'enabled' },
  { label: 'Disabled', value: 'disabled' }
]

const columns: TableColumn<ReplicationView>[] = [
  { accessorKey: 'repository', header: 'Repository' },
  { accessorKey: 'targetUrl', header: 'Target' },
  { accessorKey: 'schedule', header: 'Schedule' },
  { accessorKey: 'lastStatus', header: 'Last run' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-replications" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Replication">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'replication') }}</span>
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
            label="New replication"
            :disabled="!available.length"
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
          placeholder="Repository or target…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="lastStatus"
          aria-label="Filter by last status"
          :items="statusItems"
          size="sm"
          class="w-40"
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
        what="replications"
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
        <template #repository-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.repository }}</span>
          <UBadge
            v-if="!row.original.enabled"
            class="ms-2"
            color="neutral"
            variant="subtle"
            label="Disabled"
          />
        </template>
        <template #targetUrl-cell="{ row }">
          <span class="text-default">{{ row.original.targetUrl }}</span>
          <div v-if="row.original.username" class="text-xs text-muted">
            as {{ row.original.username }}
          </div>
        </template>
        <template #schedule-cell="{ row }">
          <span class="font-mono text-xs">{{ row.original.schedule }}</span>
          <div v-if="row.original.onChange" class="text-xs text-muted">
            and on every deploy
          </div>
        </template>
        <template #lastStatus-cell="{ row }">
          <UTooltip :text="row.original.lastError" :disabled="!row.original.lastError">
            <UBadge :color="lastRun[row.original.lastStatus].color" variant="subtle" :label="lastRun[row.original.lastStatus].label" />
          </UTooltip>
          <div v-if="row.original.lastRun" class="text-xs text-muted">
            {{ formatDate(row.original.lastRun) }}
          </div>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-play"
              color="neutral"
              variant="ghost"
              aria-label="Run now"
              @click="run(row.original)"
            />
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
            icon="i-lucide-arrow-right-left"
            :title="active ? 'No replication matches these filters' : 'No replications'"
            :description="active ? undefined : 'Push replication copies a local repository to a repository on another server.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="replications"
      />
    </template>
  </UDashboardPanel>
</template>
