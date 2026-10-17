<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { PolicyDecisionKind } from '~/gen/relic/v1/policy_decision_pb'
import type { PolicyDecisionView } from '~/utils/view-models'

definePageMeta({ layout: 'dashboard' })

const { policyDecisions } = useRelic()

const kind = ref<PolicyDecisionKind>(PolicyDecisionKind.UNSPECIFIED)
const kindItems = [
  { label: 'All', value: PolicyDecisionKind.UNSPECIFIED },
  { label: 'Downloads', value: PolicyDecisionKind.DOWNLOAD },
  { label: 'Builds', value: PolicyDecisionKind.BUILD },
  { label: 'Promotions', value: PolicyDecisionKind.RELEASE }
]

const { data: decisions, status, refresh, error } = await useAsyncData(
  () => `security:decisions:${kind.value}`,
  async () => (await policyDecisions.listPolicyDecisions({ kind: kind.value })).decisions.map(policyDecisionView)
)

const columns: TableColumn<PolicyDecisionView>[] = [
  { accessorKey: 'kind', header: 'Blocked' },
  { accessorKey: 'subject', header: 'Subject' },
  { accessorKey: 'policy', header: 'Policy' },
  { accessorKey: 'reason', header: 'Reason' },
  { accessorKey: 'count', header: 'Times' },
  { accessorKey: 'lastSeen', header: 'Last' }
]
</script>

<template>
  <UDashboardPanel id="security-decisions">
    <template #header>
      <UDashboardNavbar title="Decisions">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="decisions" class="text-xs text-muted tabular-nums">{{ pluralize(decisions.length, 'decision') }}</span>
        </template>
        <template #right>
          <USelect
            v-model="kind"
            :items="kindItems"
            aria-label="Kind"
            class="w-36"
          />
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
      <p class="text-sm text-muted">
        Policy decisions that blocked a download, failed a build or refused a promotion, counted each time they were
        taken.
      </p>

      <LoadError :error="error" what="the decisions" @retry="refresh()" />

      <UTable
        :data="decisions ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #kind-cell="{ row }">
          <UBadge color="neutral" variant="subtle" :label="row.original.kind" />
        </template>
        <template #subject-cell="{ row }">
          <span class="break-all font-mono text-sm">{{ row.original.subject }}</span>
        </template>
        <template #policy-cell="{ row }">
          <span>{{ row.original.policy }}</span>
          <div v-if="row.original.watch || row.original.rule" class="text-xs text-muted">
            <span v-if="row.original.watch">watch {{ row.original.watch }}</span>
            <span v-if="row.original.watch && row.original.rule"> · </span>
            <span v-if="row.original.rule">{{ row.original.rule }}</span>
          </div>
        </template>
        <template #reason-cell="{ row }">
          <span class="text-sm">{{ row.original.reason }}</span>
          <div class="text-xs text-muted">
            for {{ row.original.actor }}
          </div>
        </template>
        <template #count-cell="{ row }">
          <span class="tabular-nums">{{ row.original.count }}</span>
        </template>
        <template #lastSeen-cell="{ row }">
          <span class="tabular-nums text-muted">{{ formatDate(row.original.lastSeen ?? '') }}</span>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-gavel"
            title="No blocking decisions"
            description="Policies that block downloads, fail builds or gate promotions record their decisions here."
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
