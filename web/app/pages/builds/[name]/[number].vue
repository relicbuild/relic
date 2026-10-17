<script setup lang="ts">
import type { TabsItem } from '@nuxt/ui'

definePageMeta({ layout: 'dashboard' })

const route = useRoute()
const name = computed(() => String(route.params.name))
const number = computed(() => String(route.params.number))
const project = computed(() => typeof route.query.project === 'string' ? route.query.project : '')
const relic = useRelic()
const { data: build, status, error, refresh } = await useAsyncData(
  () => `build:${project.value}/${name.value}/${number.value}`,
  async () => buildInfo(await relic.builds.getBuildRun({ project: project.value, name: name.value, number: number.value })),
  { watch: [project, name, number] }
)

const artifacts = computed(() => (build.value?.modules ?? []).flatMap(m =>
  (m.artifacts ?? []).map(a => ({ module: m.id, name: a.name, type: a.type ?? '', path: a.path ?? '' }))))
const dependencies = computed(() => (build.value?.modules ?? []).flatMap(m =>
  (m.dependencies ?? []).map(d => ({ module: m.id, id: d.id, type: d.type ?? '' }))))

const tabs = computed<TabsItem[]>(() => [
  { label: 'Artifacts', icon: 'i-lucide-package', slot: 'artifacts', badge: artifacts.value.length },
  { label: 'Dependencies', icon: 'i-lucide-network', slot: 'dependencies', badge: dependencies.value.length },
  { label: 'Details', icon: 'i-lucide-info', slot: 'details' }
])
</script>

<template>
  <UDashboardPanel id="build">
    <template #header>
      <UDashboardNavbar :title="`${name} #${number}`">
        <template #leading>
          <UDashboardSidebarCollapse />
          <UButton
            icon="i-lucide-arrow-left"
            color="neutral"
            variant="ghost"
            :to="buildRunsLink(name, project)"
            aria-label="All runs"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <LoadError
        v-if="error"
        :error="error"
        what="the build"
        @retry="refresh()"
      />
      <USkeleton v-else-if="status === 'pending'" class="h-48" />
      <UTabs v-else-if="build" :items="tabs" variant="link">
        <template #artifacts>
          <UTable :data="artifacts" :columns="[{ accessorKey: 'name', header: 'Name' }, { accessorKey: 'type', header: 'Type' }, { accessorKey: 'path', header: 'Path' }, { accessorKey: 'module', header: 'Module' }]" />
        </template>
        <template #dependencies>
          <UTable :data="dependencies" :columns="[{ accessorKey: 'id', header: 'Dependency' }, { accessorKey: 'type', header: 'Type' }, { accessorKey: 'module', header: 'Module' }]" />
        </template>
        <template #details>
          <dl class="grid grid-cols-1 gap-x-6 gap-y-3 py-4 sm:grid-cols-[max-content_1fr]">
            <dt class="text-sm text-muted">
              Started
            </dt>
            <dd class="text-sm text-default">
              {{ formatDate(build.started) }}
            </dd>
            <template v-if="build.agent">
              <dt class="text-sm text-muted">
                Agent
              </dt>
              <dd class="text-sm text-default">
                {{ build.agent.name }} {{ build.agent.version }}
              </dd>
            </template>
            <template v-for="(v, i) in build.vcs ?? []" :key="i">
              <dt class="text-sm text-muted">
                VCS
              </dt>
              <dd class="font-mono text-sm break-all text-default">
                {{ v.url }} {{ v.branch }} {{ v.revision }}
              </dd>
            </template>
          </dl>
        </template>
      </UTabs>
    </template>
  </UDashboardPanel>
</template>
