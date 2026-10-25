<script setup lang="ts">
definePageMeta({ layout: 'dashboard' })

const { system } = useRelic()
const { views } = useSession()
const { data: stats, status, error, refresh } = await useAsyncData('stats', async () => {
  const o = await system.getDashboardSummary({})
  return { repositories: o.repositories, builds: toNumber(o.builds), scans: o.scans, findings: o.findings, vulnerabilityDatabaseBuilt: o.vulnerabilityDatabaseBuilt ? toIsoDate(o.vulnerabilityDatabaseBuilt) : undefined, scannerAvailable: o.scannerAvailable }
})

const repoCount = computed(() => Object.values(stats.value?.repositories ?? {}).reduce((a, b) => a + (b ?? 0), 0))
const scanned = computed(() => (stats.value?.scans.done ?? 0) + (stats.value?.scans.partial ?? 0))
const backlog = computed(() => stats.value?.scans.queued ?? 0)
const scanState = computed(() => {
  if (!stats.value?.scannerAvailable) {
    return 'Relic Scan is off: no worker with Syft and Grype reported lately'
  }
  return backlog.value ? `${backlog.value} waiting to be scanned` : 'Scan backlog is empty'
})
const shown = severities.filter(s => s !== 'unknown')
</script>

<template>
  <UDashboardPanel id="home">
    <template #header>
      <UDashboardNavbar title="Home">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <LoadError :error="error" what="the summary" @retry="refresh()" />
      <UPageGrid v-if="stats" :class="views ? 'lg:grid-cols-4' : 'lg:grid-cols-2'">
        <UPageCard
          icon="i-lucide-boxes"
          title="Repositories"
          :description="`${stats.repositories.local ?? 0} local · ${stats.repositories.remote ?? 0} remote · ${stats.repositories.virtual ?? 0} virtual`"
          to="/repositories"
          variant="subtle"
        >
          <span class="text-3xl font-semibold text-highlighted">{{ repoCount }}</span>
        </UPageCard>

        <UPageCard
          icon="i-lucide-hammer"
          title="Builds"
          description="Published build names"
          to="/builds"
          variant="subtle"
        >
          <span class="text-3xl font-semibold text-highlighted">{{ stats.builds }}</span>
        </UPageCard>

        <UPageCard
          v-if="views"
          icon="i-lucide-scan-search"
          title="Scanned artifacts"
          :description="scanState"
          variant="subtle"
        >
          <span class="text-3xl font-semibold text-highlighted">{{ scanned }}</span>
        </UPageCard>

        <UPageCard
          v-if="views"
          icon="i-lucide-shield-alert"
          title="Vulnerability findings"
          :description="stats.vulnerabilityDatabaseBuilt ? `Database built ${formatDate(stats.vulnerabilityDatabaseBuilt)}` : 'No vulnerability database yet'"
          variant="subtle"
        >
          <div class="flex flex-wrap gap-2">
            <UBadge
              v-for="s in shown"
              :key="s"
              :color="severityColor(s)"
              variant="subtle"
              :label="`${severityLabel(s)} ${stats.findings[s] ?? 0}`"
            />
          </div>
        </UPageCard>
      </UPageGrid>

      <div v-else-if="status === 'pending'" class="grid gap-4" :class="views ? 'lg:grid-cols-4' : 'lg:grid-cols-2'">
        <USkeleton v-for="i in (views ? 4 : 2)" :key="i" class="h-32" />
      </div>
    </template>
  </UDashboardPanel>
</template>
