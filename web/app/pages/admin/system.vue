<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { JobKindView, JobState, SystemView } from '~/utils/view-models'
import { systemView } from '~/utils/mappers'
import { AdminJobKindSlideover } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { system } = useRelic()
const toast = useToast()
const jobKind = useOverlay().create(AdminJobKindSlideover)
const { data, status, error, refresh } = await useAsyncData('admin:system', async () => systemView(await system.getSystemStatus({})))

const queuing = ref(false)
async function runGC() {
  queuing.value = true
  try {
    await system.collectGarbage({})
    toast.add({ title: 'Garbage collection queued', description: 'The worker runs it next.', color: 'success', icon: 'i-lucide-check-circle' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not queue garbage collection', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    queuing.value = false
  }
}

const checking = ref(false)
async function runStorageCheck() {
  checking.value = true
  try {
    const { jobId } = await system.checkStorage({})
    toast.add({ title: 'Storage check queued', description: `Job ${jobId}: its report is in the storage_check job's details.`, color: 'success', icon: 'i-lucide-check-circle' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not queue the storage check', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    checking.value = false
  }
}

const archive = ref<File | null>(null)
const importing = ref(false)
const importProgress = ref(0)
watch(archive, async (file) => {
  if (!file) {
    return
  }
  importing.value = true
  importProgress.value = 0
  try {
    await importVulnerabilityDatabase(file, (f) => {
      importProgress.value = f
    })
    toast.add({ title: 'Vulnerability database uploaded', description: 'The worker installs it and re-matches what was scanned.', color: 'success', icon: 'i-lucide-check-circle' })
  } catch (err) {
    toast.add({ title: 'Could not import the vulnerability database', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    importing.value = false
    archive.value = null
  }
})

const checks: Record<string, { title: string, icon: string }> = {
  'database': { title: 'Database', icon: 'i-lucide-database' },
  'object storage': { title: 'Object storage', icon: 'i-lucide-hard-drive' }
}

const stateColumns: { state: JobState, label: string, color: 'neutral' | 'info' | 'warning' | 'error' | 'success' }[] = [
  { state: 'available', label: 'Waiting', color: 'neutral' },
  { state: 'running', label: 'Running', color: 'info' },
  { state: 'retryable', label: 'Retrying', color: 'warning' },
  { state: 'discarded', label: 'Failed', color: 'error' },
  { state: 'completed', label: 'Done', color: 'success' }
]

const workerColumns: TableColumn<SystemView['workers'][number]>[] = [
  { accessorKey: 'id', header: 'Worker' },
  { accessorKey: 'running', header: 'Running' },
  { accessorKey: 'lastActive', header: 'Last started a job' }
]
const queueColumns: TableColumn<SystemView['queues'][number]>[] = [
  { accessorKey: 'name', header: 'Queue' },
  { accessorKey: 'waiting', header: 'Waiting' },
  { accessorKey: 'running', header: 'Running' },
  { accessorKey: 'updated', header: 'Last report' }
]

const columns: TableColumn<JobKindView>[] = [
  { accessorKey: 'kind', header: 'Job' },
  ...stateColumns.map(s => ({ id: s.state, header: s.label })),
  { accessorKey: 'lastFinished', header: 'Last finished' },
  { accessorKey: 'lastError', header: 'Last error' }
]
</script>

<template>
  <UDashboardPanel id="admin-system">
    <template #header>
      <UDashboardNavbar title="System">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <UButton
            icon="i-lucide-refresh-cw"
            color="neutral"
            variant="outline"
            label="Refresh"
            :loading="status === 'pending'"
            @click="refresh()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <LoadError :error="error" what="the system status" @retry="refresh()" />
      <template v-if="data">
        <UPageGrid class="lg:grid-cols-4">
          <UPageCard
            v-for="c in data.checks"
            :key="c.name"
            :icon="checks[c.name]?.icon ?? 'i-lucide-activity'"
            :title="checks[c.name]?.title ?? c.name"
            :description="c.ok ? `Answered in ${c.latencyMs} ms` : c.error"
            variant="subtle"
            :ui="{ description: 'break-words' }"
          >
            <UBadge
              :color="c.ok ? 'success' : 'error'"
              variant="subtle"
              :icon="c.ok ? 'i-lucide-circle-check' : 'i-lucide-circle-x'"
              :label="c.ok ? 'Healthy' : 'Unavailable'"
              class="w-fit"
            />
          </UPageCard>

          <UPageCard
            icon="i-lucide-package"
            title="Stored content"
            :description="`${pluralize(data.storage.blobs, 'blob')}, ${formatBytes(data.storage.unreferencedBytes)} awaiting garbage collection`"
            variant="subtle"
          >
            <span class="text-3xl font-semibold text-highlighted">{{ formatBytes(data.storage.bytes) }}</span>
            <div class="flex flex-wrap gap-2">
              <UButton
                v-if="administers"
                icon="i-lucide-trash-2"
                color="neutral"
                variant="outline"
                label="Run garbage collection"
                :loading="queuing"
                @click="runGC"
              />
              <UButton
                v-if="administers"
                icon="i-lucide-shield-check"
                color="neutral"
                variant="outline"
                label="Check storage"
                :loading="checking"
                @click="runStorageCheck"
              />
            </div>
          </UPageCard>

          <UPageCard
            icon="i-lucide-scan-search"
            title="Relic Scan"
            :description="!data.scanner.available ? 'Off: no worker with Syft and Grype reported lately' : data.scanner.vulnerabilityDatabaseBuilt ? `Vulnerability database built ${formatDate(data.scanner.vulnerabilityDatabaseBuilt)}` : 'No vulnerability database yet'"
            variant="subtle"
          >
            <div class="flex flex-wrap gap-2">
              <UBadge color="neutral" variant="subtle" :label="`Queued ${data.scanner.scans.queued ?? 0}`" />
              <UBadge color="success" variant="subtle" :label="`Done ${(data.scanner.scans.done ?? 0) + (data.scanner.scans.partial ?? 0)}`" />
              <UBadge color="error" variant="subtle" :label="`Failed ${data.scanner.scans.failed ?? 0}`" />
            </div>
            <UAlert
              v-if="data.scanner.importError"
              color="error"
              variant="subtle"
              icon="i-lucide-database-zap"
              :title="`The latest database import failed${data.scanner.importFailed ? ` ${formatDate(data.scanner.importFailed)}` : ''}`"
              :description="data.scanner.importError"
              :ui="{ description: 'break-words' }"
            />
            <UFileUpload
              v-if="administers"
              v-slot="{ open }"
              v-model="archive"
              :preview="false"
              :disabled="importing"
              accept=".zst,.tar,.gz,.xz"
            >
              <UButton
                icon="i-lucide-database-backup"
                color="neutral"
                variant="outline"
                :label="importing ? `Uploading ${Math.round(importProgress * 100)}%` : 'Import vulnerability database'"
                class="w-fit"
                :loading="importing"
                @click="open()"
              />
            </UFileUpload>
          </UPageCard>
        </UPageGrid>

        <h2 class="mt-6 text-sm font-semibold text-highlighted">
          Background jobs
        </h2>
        <p class="mb-2 text-sm text-muted">
          Finished jobs are kept for 24 hours.
        </p>
        <UTable
          :data="data.jobs"
          :columns="columns"
          :loading="status === 'pending'"
          class="shrink-0 cursor-pointer"
          @select="(_e: Event, row: { original: JobKindView }) => jobKind.open({ kind: row.original.kind })"
        >
          <template #kind-cell="{ row }">
            <span class="font-mono text-highlighted">{{ row.original.kind }}</span>
          </template>
          <template v-for="s in stateColumns" :key="s.state" #[`${s.state}-cell`]="{ row }">
            <UBadge
              v-if="row.original.states[s.state]"
              :color="s.color"
              variant="subtle"
              :label="String(row.original.states[s.state])"
            />
            <span v-else class="text-dimmed">0</span>
          </template>
          <template #lastFinished-cell="{ row }">
            {{ formatDate(row.original.lastFinished) }}
          </template>
          <template #lastError-cell="{ row }">
            <span v-if="row.original.lastError" class="line-clamp-2 max-w-md whitespace-normal break-words text-error">{{ row.original.lastError }}</span>
          </template>
        </UTable>
        <p class="mt-1 text-xs text-muted">
          Choose a job to see its recent runs and every error in full.
        </p>

        <div class="mt-6 grid shrink-0 gap-6 2xl:grid-cols-2">
          <div class="min-w-0">
            <h2 class="text-sm font-semibold text-highlighted">
              Workers
            </h2>
            <p class="mb-2 text-sm text-muted">
              Servers and workers that ran jobs in the last hour. The leader runs periodic jobs.
            </p>
            <UTable :data="data.workers" :columns="workerColumns">
              <template #id-cell="{ row }">
                <span class="font-mono text-highlighted">{{ row.original.id }}</span>
                <UBadge
                  v-if="row.original.leader"
                  class="ms-2"
                  color="primary"
                  variant="subtle"
                  label="Leader"
                />
              </template>
              <template #lastActive-cell="{ row }">
                {{ row.original.lastActive ? formatDate(row.original.lastActive) : '—' }}
              </template>
              <template #empty>
                <UEmpty variant="naked" icon="i-lucide-cpu" title="No worker ran a job in the last hour" />
              </template>
            </UTable>
          </div>
          <div class="min-w-0">
            <h2 class="text-sm font-semibold text-highlighted">
              Queues
            </h2>
            <p class="mb-2 text-sm text-muted">
              Workers report on their queues every 10 minutes; a report older than that means no worker serves the queue.
            </p>
            <UTable :data="data.queues" :columns="queueColumns">
              <template #name-cell="{ row }">
                <span class="font-mono text-highlighted">{{ row.original.name }}</span>
                <UBadge
                  v-if="row.original.paused"
                  class="ms-2"
                  color="warning"
                  variant="subtle"
                  label="Paused"
                />
              </template>
              <template #updated-cell="{ row }">
                {{ row.original.updated ? formatDate(row.original.updated) : '—' }}
              </template>
            </UTable>
          </div>
        </div>
      </template>

      <div v-else-if="status === 'pending'" class="grid gap-4 lg:grid-cols-4">
        <USkeleton v-for="i in 4" :key="i" class="h-32" />
      </div>
    </template>
  </UDashboardPanel>
</template>
