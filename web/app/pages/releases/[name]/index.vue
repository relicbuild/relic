<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { ReleaseView } from '~/utils/view-models'

definePageMeta({ layout: 'dashboard' })

const route = useRoute()
const name = computed(() => String(route.params.name))
const project = computed(() => String(route.query.project ?? ''))
const { releases } = useRelic()
const { data, status, error, refresh } = await useAsyncData(
  () => `release:${project.value}/${name.value}`,
  async () => (await releases.listReleaseVersions({ project: project.value, name: name.value })).releases.map(releaseView),
  { watch: [name, project] }
)

const page = ref(1)
const pageSize = ref(50)
const all = computed<ReleaseView[]>(() => data.value ?? [])

const statusColors: Record<string, 'neutral' | 'success' | 'warning' | 'error'> = {
  completed: 'success',
  draft: 'neutral',
  processing: 'warning',
  failed: 'error',
  unknown: 'neutral'
}

const versionLink = (r: ReleaseView) => ({
  path: `/releases/${encodeURIComponent(r.name)}/${encodeURIComponent(r.version)}`,
  query: r.project ? { project: r.project } : {}
})

const columns: TableColumn<ReleaseView>[] = [
  { accessorKey: 'version', header: 'Version' },
  { accessorKey: 'status', header: 'Status' },
  { accessorKey: 'artifacts', header: 'Artifacts' },
  { accessorKey: 'createdBy', header: 'Created by' },
  { accessorKey: 'created', header: 'Created' }
]
</script>

<template>
  <UDashboardPanel id="release-versions" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar :title="name">
        <template #leading>
          <UDashboardSidebarCollapse />
          <UButton
            icon="i-lucide-arrow-left"
            color="neutral"
            variant="ghost"
            to="/releases"
            aria-label="All releases"
          />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'version') }}</span>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <LoadError
        :error="error"
        what="the release's versions"
        class="m-4"
        @retry="refresh()"
      />
      <UTable
        :data="paginate(all, page, pageSize)"
        :columns="columns"
        :loading="status === 'pending'"
        sticky
        :ui="denseTable"
      >
        <template #version-cell="{ row }">
          <ULink :to="versionLink(row.original)" class="font-mono font-medium text-highlighted">
            {{ row.original.version }}
          </ULink>
          <UBadge
            v-if="row.original.tag"
            class="ms-2"
            color="neutral"
            variant="outline"
            :label="row.original.tag"
          />
        </template>
        <template #status-cell="{ row }">
          <UBadge :color="statusColors[row.original.status]" variant="subtle" :label="row.original.status" />
        </template>
        <template #artifacts-cell="{ row }">
          <span class="tabular-nums">{{ row.original.artifacts.length }}</span>
        </template>
        <template #created-cell="{ row }">
          {{ formatDate(row.original.created) }}
        </template>
        <template #empty>
          <UEmpty variant="naked" icon="i-lucide-package-check" title="No versions" />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="all.length"
        noun="versions"
      />
    </template>
  </UDashboardPanel>
</template>
