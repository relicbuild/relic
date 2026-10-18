<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { BuildView } from '~/utils/view-models'

definePageMeta({ layout: 'dashboard' })

const route = useRoute()
const router = useRouter()
const relic = useRelic()
const noProject = 'none'
const project = computed({
  get: () => typeof route.query.project === 'string' && route.query.project ? route.query.project : noProject,
  set: (key: string) => router.replace(buildsLink(key === noProject ? '' : key))
})
const projectKey = computed(() => project.value === noProject ? '' : project.value)
const { data: projects } = await useAsyncData('builds:projects', async () =>
  (await relic.projects.listProjects({})).projects.map(p => p.key), { default: () => [] })
const projectItems = computed(() => [
  { label: 'Outside projects', value: noProject },
  ...projects.value.map(key => ({ label: key, value: key }))
])
const { data: builds, status, error, refresh } = await useAsyncData(
  () => `builds:${projectKey.value}`,
  async () => (await relic.builds.listBuilds({ project: projectKey.value })).builds
    .map((b): BuildView => ({ name: b.name, lastStarted: toIsoDate(b.lastStarted) })),
  { watch: [projectKey] }
)
const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const since = ref('all')
watch([search, since, projectKey], () => {
  page.value = 1
})
const sinceItems = [
  { label: 'Any time', value: 'all' },
  { label: 'Last 24 hours', value: '1' },
  { label: 'Last 7 days', value: '7' },
  { label: 'Last 30 days', value: '30' }
]
const recent = (when: string) => since.value === 'all' || Date.now() - new Date(when).getTime() <= Number(since.value) * 86_400_000
const all = computed<BuildView[]>(() => builds.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(b => (!q || b.name.toLowerCase().includes(q)) && recent(b.lastStarted))
})
const active = computed(() => [search.value.trim() !== '', since.value !== 'all'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  since.value = 'all'
}

const columns: TableColumn<BuildView>[] = [
  { accessorKey: 'name', header: 'Build' },
  { accessorKey: 'lastStarted', header: 'Last run' }
]
</script>

<template>
  <UDashboardPanel id="builds" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Builds">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="builds" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'build') }}</span>
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
          placeholder="Build name…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-if="projects.length"
          v-model="project"
          aria-label="Filter by project"
          :items="projectItems"
          icon="i-lucide-folder-kanban"
          size="sm"
          class="w-44"
        />
        <USelect
          v-model="since"
          aria-label="Filter by start"
          :items="sinceItems"
          icon="i-lucide-calendar"
          size="sm"
          class="w-40"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="builds"
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
          <ULink :to="buildRunsLink(row.original.name, projectKey)" class="font-medium text-highlighted">
            {{ row.original.name }}
          </ULink>
        </template>
        <template #lastStarted-cell="{ row }">
          {{ formatDate(row.original.lastStarted) }}
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-hammer"
            :title="active ? 'No build matches these filters' : 'No builds'"
            :description="active ? undefined : 'Build runs appear here once a CI client publishes their build info.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="builds"
      />
    </template>
  </UDashboardPanel>
</template>
