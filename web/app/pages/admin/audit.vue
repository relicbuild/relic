<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import type { AuditEventView, AuditOutcomeName } from '~/utils/view-models'
import { auditEventView, auditOutcomeOf } from '~/utils/mappers'

definePageMeta({ layout: 'dashboard' })

const { audit } = useRelic()
const page = ref(1)
const pageSize = ref(50)
const actorSearch = ref('')
const actor = useDebounced(actorSearch)
const actionSearch = ref('')
const action = useDebounced(actionSearch)
const targetSearch = ref('')
const target = useDebounced(targetSearch)
const outcome = ref<'all' | AuditOutcomeName>('all')
const period = ref('7d')

const periods = [
  { label: 'Last 24 hours', value: '1d' },
  { label: 'Last 7 days', value: '7d' },
  { label: 'Last 30 days', value: '30d' },
  { label: 'Everything kept', value: 'all' }
]
const outcomes = [
  { label: 'All outcomes', value: 'all' },
  { label: 'Succeeded', value: 'success' },
  { label: 'Denied', value: 'denied' },
  { label: 'Failed', value: 'failure' }
]

const filters = [actor, action, target, outcome, period]
watch(filters, () => {
  page.value = 1
})
const active = computed(() =>
  [actorSearch.value.trim() !== '', actionSearch.value.trim() !== '', targetSearch.value.trim() !== '', outcome.value !== 'all', period.value !== '7d'].filter(Boolean).length
)
function clearFilters() {
  actorSearch.value = actionSearch.value = targetSearch.value = ''
  outcome.value = 'all'
  period.value = '7d'
}

function since() {
  const days = Number.parseInt(period.value)
  return Number.isNaN(days) ? undefined : timestampFromDate(new Date(Date.now() - days * 24 * 60 * 60 * 1000))
}

const { data, status, error, refresh } = await useAsyncData(
  'admin:audit',
  async () => {
    const r = await audit.listAuditEvents({
      actor: actor.value.trim(),
      action: action.value.trim(),
      target: target.value.trim(),
      outcome: outcome.value === 'all' ? undefined : auditOutcomeOf(outcome.value),
      since: since(),
      page: page.value,
      pageSize: pageSize.value
    })
    return { items: r.events.map(auditEventView), total: toNumber(r.total) }
  },
  { watch: [page, pageSize, ...filters] }
)

const outcomeBadges: Record<AuditOutcomeName, { label: string, color: 'success' | 'warning' | 'error' }> = {
  success: { label: 'Succeeded', color: 'success' },
  denied: { label: 'Denied', color: 'warning' },
  failure: { label: 'Failed', color: 'error' }
}

const columns: TableColumn<AuditEventView>[] = [
  { accessorKey: 'time', header: 'Time' },
  { accessorKey: 'actor', header: 'Actor' },
  { accessorKey: 'action', header: 'Action' },
  { accessorKey: 'target', header: 'Target' },
  { accessorKey: 'outcome', header: 'Outcome' },
  { accessorKey: 'client', header: 'Client' }
]
</script>

<template>
  <UDashboardPanel id="admin-audit" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Audit log">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(data.total, 'event') }}</span>
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
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters :active="active" :matched="data?.total" @clear="clearFilters">
        <UInput
          v-model="actorSearch"
          icon="i-lucide-user"
          placeholder="User"
          aria-label="Filter by user"
          size="sm"
          class="w-full sm:w-40"
        />
        <UInput
          v-model="actionSearch"
          icon="i-lucide-search"
          placeholder="Action contains…"
          aria-label="Filter by action"
          size="sm"
          class="w-full sm:w-52"
        />
        <UInput
          v-model="targetSearch"
          icon="i-lucide-crosshair"
          placeholder="Target contains…"
          aria-label="Filter by target"
          size="sm"
          class="w-full sm:w-52"
        />
        <USelect
          v-model="outcome"
          aria-label="Filter by outcome"
          :items="outcomes"
          size="sm"
          class="w-36"
        />
        <USelect
          v-model="period"
          aria-label="Period"
          :items="periods"
          icon="i-lucide-calendar"
          size="sm"
          class="w-44"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="the audit log"
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
        <template #time-cell="{ row }">
          <span class="whitespace-nowrap tabular-nums text-muted">{{ formatDate(row.original.time) }}</span>
        </template>
        <template #actor-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.actor }}</span>
        </template>
        <template #action-cell="{ row }">
          <code class="text-xs">{{ row.original.action }}</code>
        </template>
        <template #target-cell="{ row }">
          <span class="break-all text-xs">{{ row.original.target }}</span>
        </template>
        <template #outcome-cell="{ row }">
          <UTooltip :text="row.original.detail" :disabled="!row.original.detail">
            <UBadge
              :color="outcomeBadges[row.original.outcome].color"
              variant="subtle"
              size="sm"
              :label="outcomeBadges[row.original.outcome].label"
            />
          </UTooltip>
        </template>
        <template #client-cell="{ row }">
          <span class="font-mono text-xs text-muted">{{ row.original.client }}</span>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-scroll-text"
            :title="active ? 'No event matches these filters' : 'No events'"
            :description="active ? undefined : 'Changes to settings, sign-ins, tokens and blocked downloads appear here.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="data?.total ?? 0"
        noun="events"
      />
    </template>
  </UDashboardPanel>
</template>
