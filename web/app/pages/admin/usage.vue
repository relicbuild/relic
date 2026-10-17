<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { DayUsage, RepositoryUsage, UserUsage } from '~/gen/relic/v1/system_pb'

definePageMeta({ layout: 'dashboard' })

const { system } = useRelic()
const days = ref(30)
const periods = [
  { label: 'Last 7 days', value: 7 },
  { label: 'Last 30 days', value: 30 },
  { label: 'Last 90 days', value: 90 },
  { label: 'Last year', value: 365 }
]

const { data, status, error, refresh } = await useAsyncData(
  () => `admin:usage:${days.value}`,
  async () => await system.getUsageReport({ days: days.value }),
  { watch: [days] }
)

const charts = computed(() => {
  const list: DayUsage[] = data.value?.days ?? []
  const peak = (pick: (d: DayUsage) => number) => Math.max(1, ...list.map(pick))
  return [
    { title: 'Stored', pick: (d: DayUsage) => toNumber(d.bytes), format: (n: number) => formatBytes(n) },
    { title: 'Downloads', pick: (d: DayUsage) => toNumber(d.downloads), format: (n: number) => n.toLocaleString() },
    { title: 'Deploys', pick: (d: DayUsage) => toNumber(d.deploys), format: (n: number) => n.toLocaleString() }
  ].map(c => ({
    ...c,
    peak: peak(c.pick),
    total: c.title === 'Stored' ? (list.length ? c.pick(list[list.length - 1]!) : 0) : list.reduce((sum, d) => sum + c.pick(d), 0)
  }))
})

const repositoryColumns: TableColumn<RepositoryUsage>[] = [
  { accessorKey: 'repository', header: 'Repository' },
  { accessorKey: 'files', header: 'Files' },
  { accessorKey: 'bytes', header: 'Size' },
  { accessorKey: 'growthBytes', header: 'Growth' },
  { accessorKey: 'downloads', header: 'Downloads' },
  { accessorKey: 'deploys', header: 'Deploys' }
]

const top = computed(() => [
  { title: 'Top downloaders', users: data.value?.topDownloaders ?? [], count: (u: UserUsage) => toNumber(u.downloads) },
  { title: 'Top deployers', users: data.value?.topDeployers ?? [], count: (u: UserUsage) => toNumber(u.deploys) }
])
</script>

<template>
  <UDashboardPanel id="admin-usage">
    <template #header>
      <UDashboardNavbar title="Usage">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ data.from }} to {{ data.to }}</span>
        </template>
        <template #right>
          <USelect
            v-model="days"
            :items="periods"
            aria-label="Period"
            class="w-40"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <LoadError :error="error" what="the usage report" @retry="refresh()" />
      <USkeleton v-if="status === 'pending' && !data" class="h-48" />
      <template v-else-if="data">
        <div class="grid gap-4 lg:grid-cols-3">
          <UCard v-for="chart in charts" :key="chart.title">
            <div class="text-sm text-muted">
              {{ chart.title }}
            </div>
            <div class="text-2xl font-semibold text-highlighted tabular-nums">
              {{ chart.format(chart.total) }}
            </div>
            <div class="mt-3 flex h-24 items-end gap-px" :aria-label="`${chart.title} by day`" role="img">
              <div
                v-for="d in data.days"
                :key="d.day"
                class="min-w-0.5 flex-1 rounded-t-sm bg-primary/70"
                :style="{ height: `${Math.max(2, chart.pick(d) / chart.peak * 100)}%` }"
                :title="`${d.day}: ${chart.format(chart.pick(d))}`"
              />
            </div>
          </UCard>
        </div>

        <UTable :data="data.repositories" :columns="repositoryColumns" class="shrink-0">
          <template #repository-cell="{ row }">
            <ULink :to="`/repositories/${row.original.repository}`" class="font-medium text-highlighted">
              {{ row.original.repository }}
            </ULink>
            <UBadge
              v-if="row.original.project"
              class="ms-2"
              color="neutral"
              variant="subtle"
              :label="row.original.project"
            />
          </template>
          <template #files-cell="{ row }">
            <span class="tabular-nums">{{ toNumber(row.original.files).toLocaleString() }}</span>
          </template>
          <template #bytes-cell="{ row }">
            <span class="tabular-nums">{{ formatBytes(toNumber(row.original.bytes)) }}</span>
          </template>
          <template #growthBytes-cell="{ row }">
            <span class="tabular-nums text-muted">{{ toNumber(row.original.growthBytes) >= 0 ? '+' : '−' }}{{ formatBytes(Math.abs(toNumber(row.original.growthBytes))) }}</span>
          </template>
          <template #downloads-cell="{ row }">
            <span class="tabular-nums">{{ toNumber(row.original.downloads).toLocaleString() }}</span>
          </template>
          <template #deploys-cell="{ row }">
            <span class="tabular-nums">{{ toNumber(row.original.deploys).toLocaleString() }}</span>
          </template>
          <template #empty>
            <UEmpty
              variant="naked"
              icon="i-lucide-chart-column"
              title="No usage recorded yet"
              description="The worker records each repository's size every six hours; downloads and deploys are counted as they happen."
            />
          </template>
        </UTable>

        <div class="grid gap-4 lg:grid-cols-2">
          <UCard v-for="list in top" :key="list.title">
            <template #header>
              <span class="text-sm font-semibold text-highlighted">{{ list.title }}</span>
            </template>
            <ul v-if="list.users.length" class="space-y-1 text-sm">
              <li v-for="u in list.users" :key="u.user" class="flex justify-between">
                <span>{{ u.user }}</span>
                <span class="tabular-nums text-muted">{{ list.count(u).toLocaleString() }}</span>
              </li>
            </ul>
            <p v-else class="text-sm text-muted">
              None in this period.
            </p>
          </UCard>
        </div>
      </template>
    </template>
  </UDashboardPanel>
</template>
