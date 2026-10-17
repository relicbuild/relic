<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { QuarantineState, type QuarantinedFile } from '~/gen/relic/v1/quarantine_pb'
import { ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { quarantine } = useRelic()
const toast = useToast()
const confirm = useOverlay().create(ConfirmModal)

const { data: files, status, refresh, error } = await useAsyncData('security:quarantine', async () => (await quarantine.listQuarantined({})).files)

const states: Record<QuarantineState, { label: string, color: 'neutral' | 'warning' | 'error' }> = {
  [QuarantineState.UNSPECIFIED]: { label: 'Held', color: 'neutral' },
  [QuarantineState.AWAITING_SCAN]: { label: 'Awaiting scan', color: 'neutral' },
  [QuarantineState.SCAN_FAILED]: { label: 'Scan failed', color: 'warning' },
  [QuarantineState.SCAN_SKIPPED]: { label: 'Scan skipped', color: 'warning' },
  [QuarantineState.MALICIOUS]: { label: 'Malicious', color: 'error' },
  [QuarantineState.REJECTED]: { label: 'Rejected', color: 'error' }
}

async function decide(file: QuarantinedFile, release: boolean) {
  const what = `${file.repository}/${file.path}`
  if (!(await confirm.open({
    title: release ? `Release ${what}?` : `Reject ${what}?`,
    description: release ? 'Relic serves it whatever its scan says.' : 'Relic never serves it, whatever its scan says.',
    confirmLabel: release ? 'Release' : 'Reject'
  }).result)) {
    return
  }
  try {
    const target = { repository: file.repository, path: file.path, reason: '' }
    await (release ? quarantine.releaseQuarantined(target) : quarantine.rejectQuarantined(target))
    toast.add({ title: release ? `Released ${what}` : `Rejected ${what}`, color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not decide', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns: TableColumn<QuarantinedFile>[] = [
  { accessorKey: 'path', header: 'File' },
  { accessorKey: 'state', header: 'State' },
  { accessorKey: 'cached', header: 'Cached' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="security-quarantine">
    <template #header>
      <UDashboardNavbar title="Quarantine">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="files" class="text-xs text-muted tabular-nums">{{ pluralize(files.length, 'file') }}</span>
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
      <p class="text-sm text-muted">
        Remote repositories with quarantine on hold what they cache until Relic Scan has scanned it and found it not
        malicious. A file whose malicious finding is withdrawn is released by the next match.
      </p>

      <LoadError :error="error" what="the quarantine" @retry="refresh()" />

      <UTable
        :data="files ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #path-cell="{ row }">
          <ULink :to="{ path: `/repositories/${row.original.repository}`, query: { path: row.original.path } }" class="break-all text-sm">
            {{ row.original.repository }}/{{ row.original.path }}
          </ULink>
        </template>
        <template #state-cell="{ row }">
          <UBadge :color="states[row.original.state].color" variant="subtle" :label="states[row.original.state].label" />
          <div v-if="row.original.decidedBy" class="text-xs text-muted">
            by {{ row.original.decidedBy }}<span v-if="row.original.reason">: {{ row.original.reason }}</span>
          </div>
        </template>
        <template #cached-cell="{ row }">
          <span class="tabular-nums text-muted">{{ formatDate(row.original.cached ? toIsoDate(row.original.cached) : '') }}</span>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-shield-check"
              color="neutral"
              variant="ghost"
              label="Release"
              @click="decide(row.original, true)"
            />
            <UButton
              v-if="row.original.state !== QuarantineState.REJECTED"
              icon="i-lucide-shield-x"
              color="error"
              variant="ghost"
              label="Reject"
              @click="decide(row.original, false)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-shield-check"
            title="Nothing in quarantine"
            description="Turn on Quarantine on a remote repository to hold what it caches until it is scanned."
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
