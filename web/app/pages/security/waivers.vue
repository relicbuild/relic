<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { WaiverStatus, type Waiver } from '~/gen/relic/v1/waiver_pb'
import { ConfirmModal, SecurityWaiverDecisionModal, SecurityWaiverRequestForm } from '#components'

definePageMeta({ layout: 'dashboard' })

const { me, administers } = useSession()
const { waivers } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const requestForm = overlay.create(SecurityWaiverRequestForm)
const decision = overlay.create(SecurityWaiverDecisionModal)
const confirm = overlay.create(ConfirmModal)

const statusFilter = ref<WaiverStatus>(WaiverStatus.UNSPECIFIED)
const statusItems = [
  { label: 'All', value: WaiverStatus.UNSPECIFIED },
  { label: 'Pending', value: WaiverStatus.PENDING },
  { label: 'Approved', value: WaiverStatus.APPROVED },
  { label: 'Rejected', value: WaiverStatus.REJECTED }
]

const page = ref(1)
const pageSize = ref(50)
watch(statusFilter, () => {
  page.value = 1
})

const { data, status, refresh, error } = await useAsyncData(
  'security:waivers',
  async () => {
    const r = await waivers.listWaivers({ status: statusFilter.value, page: page.value, pageSize: pageSize.value })
    return { waivers: r.waivers, total: toNumber(r.total) }
  },
  { watch: [statusFilter, page, pageSize] }
)

const statuses: Record<WaiverStatus, { label: string, color: 'neutral' | 'success' | 'error' }> = {
  [WaiverStatus.UNSPECIFIED]: { label: 'Unknown', color: 'neutral' },
  [WaiverStatus.PENDING]: { label: 'Pending', color: 'neutral' },
  [WaiverStatus.APPROVED]: { label: 'Approved', color: 'success' },
  [WaiverStatus.REJECTED]: { label: 'Rejected', color: 'error' }
}

function filters(w: Waiver): string[] {
  const r = w.rule
  return r ? [...r.packages, ...r.vulnerabilities, ...r.cves, ...r.licenses, ...r.artifactPaths] : []
}

async function request() {
  if (await requestForm.open().result) {
    await refresh()
  }
}

async function decide(w: Waiver, approve: boolean) {
  if (await decision.open({ waiver: w, approve }).result) {
    await refresh()
  }
}

async function withdraw(w: Waiver) {
  if (!(await confirm.open({
    title: 'Withdraw the waiver?',
    description: w.reason,
    confirmLabel: 'Withdraw'
  }).result)) {
    return
  }
  try {
    await waivers.withdrawWaiver({ id: w.id })
    toast.add({ title: 'Waiver withdrawn', color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not withdraw', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns: TableColumn<Waiver>[] = [
  { id: 'filters', header: 'Waives' },
  { accessorKey: 'reason', header: 'Reason' },
  { accessorKey: 'requestedBy', header: 'Requested by' },
  { accessorKey: 'status', header: 'Status' },
  { accessorKey: 'expires', header: 'Expires' },
  { id: 'actions' }
]
</script>

<template>
  <UDashboardPanel id="security-waivers" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Waivers">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(data.total, 'waiver') }}</span>
        </template>
        <template #right>
          <UButton icon="i-lucide-plus" label="Request a waiver" @click="request()" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <TableFilters
        :active="statusFilter === WaiverStatus.UNSPECIFIED ? 0 : 1"
        :matched="data?.total"
        @clear="statusFilter = WaiverStatus.UNSPECIFIED"
      >
        <USelect
          v-model="statusFilter"
          :items="statusItems"
          aria-label="Status"
          size="sm"
          class="w-32"
        />
        <template #right>
          <span class="text-xs text-muted">A waiver asks administrators to ignore findings for a package, vulnerabilities or licenses. Approved, it becomes an ignore rule that expires.</span>
        </template>
      </TableFilters>

      <LoadError
        :error="error"
        what="the waivers"
        class="m-4"
        @retry="refresh()"
      />

      <UTable
        :data="data?.waivers ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        sticky
        :ui="denseTable"
      >
        <template #filters-cell="{ row }">
          <div class="flex flex-wrap gap-1">
            <UBadge
              v-for="f in filters(row.original)"
              :key="f"
              color="neutral"
              variant="outline"
              :label="f"
              class="font-mono"
            />
          </div>
        </template>
        <template #reason-cell="{ row }">
          <span class="text-sm">{{ row.original.reason }}</span>
        </template>
        <template #status-cell="{ row }">
          <UBadge :color="statuses[row.original.status].color" variant="subtle" :label="statuses[row.original.status].label" />
          <div v-if="row.original.decidedBy" class="text-xs text-muted">
            by {{ row.original.decidedBy }}<span v-if="row.original.decisionNote">: {{ row.original.decisionNote }}</span>
          </div>
        </template>
        <template #expires-cell="{ row }">
          <span class="tabular-nums text-muted">{{ formatDate(row.original.expires ? toIsoDate(row.original.expires) : '') }}</span>
        </template>
        <template #actions-cell="{ row }">
          <div v-if="row.original.status === WaiverStatus.PENDING" class="flex justify-end gap-1">
            <template v-if="administers">
              <UButton
                icon="i-lucide-check"
                color="neutral"
                variant="ghost"
                label="Approve"
                @click="decide(row.original, true)"
              />
              <UButton
                icon="i-lucide-x"
                color="error"
                variant="ghost"
                label="Reject"
                @click="decide(row.original, false)"
              />
            </template>
            <UButton
              v-if="row.original.requestedBy === me?.username"
              icon="i-lucide-undo-2"
              color="neutral"
              variant="ghost"
              label="Withdraw"
              @click="withdraw(row.original)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-file-check"
            title="No waivers"
            description="Request a waiver when a finding does not apply to how a package is used."
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="data?.total ?? 0"
        noun="waivers"
      />
    </template>
  </UDashboardPanel>
</template>
