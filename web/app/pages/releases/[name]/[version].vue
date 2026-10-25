<script setup lang="ts">
import type { TabsItem } from '@nuxt/ui'
import { ReleasePromoteModal } from '#components'

definePageMeta({ layout: 'dashboard' })

const route = useRoute()
const name = computed(() => String(route.params.name))
const version = computed(() => String(route.params.version))
const project = computed(() => String(route.query.project ?? ''))
const { releases, evidence } = useRelic()
const promote = useOverlay().create(ReleasePromoteModal)

const { data, status, error, refresh } = await useAsyncData(
  () => `release:${project.value}/${name.value}/${version.value}`,
  async () => {
    const key = { project: project.value, name: name.value, version: version.value }
    const [got, promotions] = await Promise.all([
      releases.getRelease(key),
      releases.listReleasePromotions(key)
    ])
    const release = releaseView(got.release!)
    const found = release.manifest
      ? (await evidence.listEvidence({ repository: release.repository, path: release.manifest })).evidence
      : []
    return { release, promotions: promotions.promotions.map(releasePromotionView), evidence: found.map(releaseEvidenceView) }
  },
  { watch: [name, version, project] }
)

const gates = computed(() => (data.value?.evidence ?? []).filter(e => e.gate))
const otherEvidence = computed(() => (data.value?.evidence ?? []).filter(e => !e.gate))

const tabs = computed<TabsItem[]>(() => [
  { label: 'Artifacts', icon: 'i-lucide-package', slot: 'artifacts', badge: data.value?.release.artifacts.length },
  { label: 'Promotions', icon: 'i-lucide-rocket', slot: 'promotions', badge: data.value?.promotions.length },
  { label: 'Gate decisions', icon: 'i-lucide-shield-check', slot: 'gates', badge: gates.value.length },
  { label: 'Evidence', icon: 'i-lucide-file-badge', slot: 'evidence', badge: otherEvidence.value.length }
])

async function openPromote() {
  if (await promote.open({ project: project.value, name: name.value, version: version.value }).result) {
    await refresh()
  }
}
</script>

<template>
  <UDashboardPanel id="release">
    <template #header>
      <UDashboardNavbar :title="`${name} ${version}`">
        <template #leading>
          <UDashboardSidebarCollapse />
          <UButton
            icon="i-lucide-arrow-left"
            color="neutral"
            variant="ghost"
            :to="{ path: `/releases/${encodeURIComponent(name)}`, query: project ? { project } : {} }"
            aria-label="All versions"
          />
        </template>
        <template #trailing>
          <UBadge
            v-if="data?.release.tag"
            color="neutral"
            variant="outline"
            :label="data.release.tag"
          />
        </template>
        <template #right>
          <UButton
            v-if="data && data.release.status === 'completed'"
            icon="i-lucide-rocket"
            label="Promote"
            @click="openPromote()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <LoadError
        v-if="error"
        :error="error"
        what="the release"
        @retry="refresh()"
      />
      <USkeleton v-else-if="status === 'pending'" class="h-48" />
      <template v-else-if="data">
        <p class="text-sm text-muted">
          {{ data.release.status }} · created by {{ data.release.createdBy }} {{ formatDate(data.release.created) }}
        </p>
        <UTabs :items="tabs" variant="link" :ui="scrollingTabs">
          <template #artifacts>
            <UTable
              :data="data.release.artifacts"
              :columns="[{ accessorKey: 'path', header: 'Path' }, { accessorKey: 'repository', header: 'From' }, { accessorKey: 'size', header: 'Size' }]"
            >
              <template #path-cell="{ row }">
                <span class="font-mono text-sm break-all">{{ row.original.path }}</span>
              </template>
              <template #size-cell="{ row }">
                <span class="tabular-nums">{{ formatBytes(row.original.size) }}</span>
              </template>
            </UTable>
          </template>
          <template #promotions>
            <UTable
              :data="data.promotions"
              :columns="[{ accessorKey: 'environment', header: 'Environment' }, { accessorKey: 'createdBy', header: 'By' }, { accessorKey: 'created', header: 'When' }, { accessorKey: 'messages', header: 'Notes' }]"
            >
              <template #environment-cell="{ row }">
                <UBadge color="primary" variant="subtle" :label="row.original.environment" />
              </template>
              <template #created-cell="{ row }">
                {{ formatDate(row.original.created) }}
              </template>
              <template #messages-cell="{ row }">
                <span class="text-xs text-muted">{{ row.original.messages.join('; ') }}</span>
              </template>
              <template #empty>
                <UEmpty variant="naked" icon="i-lucide-rocket" title="Not promoted yet" />
              </template>
            </UTable>
          </template>
          <template #gates>
            <UTable
              :data="gates"
              :columns="[{ id: 'gate', header: 'Gate' }, { id: 'decision', header: 'Decision' }, { accessorKey: 'createdBy', header: 'For' }, { accessorKey: 'created', header: 'When' }]"
            >
              <template #gate-cell="{ row }">
                <span class="font-medium">{{ row.original.gate?.name }}</span>
                <span class="text-xs text-muted"> → {{ row.original.gate?.environment }}</span>
              </template>
              <template #decision-cell="{ row }">
                <UBadge
                  :color="row.original.gate?.allowed ? 'success' : 'error'"
                  variant="subtle"
                  :label="row.original.gate?.allowed ? 'Allowed' : 'Refused'"
                />
                <div v-if="row.original.gate?.error" class="text-xs text-error">
                  {{ row.original.gate.error }}
                </div>
              </template>
              <template #created-cell="{ row }">
                {{ formatDate(row.original.created) }}
              </template>
              <template #empty>
                <UEmpty
                  variant="naked"
                  icon="i-lucide-shield-check"
                  title="No gate decisions"
                  description="Promotion gates record their decisions here when the version is promoted."
                />
              </template>
            </UTable>
          </template>
          <template #evidence>
            <UTable
              :data="otherEvidence"
              :columns="[{ accessorKey: 'predicateType', header: 'Predicate' }, { accessorKey: 'verified', header: 'Signature' }, { accessorKey: 'createdBy', header: 'By' }, { accessorKey: 'created', header: 'When' }]"
            >
              <template #predicateType-cell="{ row }">
                <span class="font-mono text-xs break-all">{{ row.original.predicateType }}</span>
              </template>
              <template #verified-cell="{ row }">
                <UBadge
                  :color="row.original.verified ? 'success' : 'neutral'"
                  variant="subtle"
                  :label="row.original.verified ? 'Verified' : 'Not verified'"
                />
              </template>
              <template #created-cell="{ row }">
                {{ formatDate(row.original.created) }}
              </template>
              <template #empty>
                <UEmpty variant="naked" icon="i-lucide-file-badge" title="No evidence" />
              </template>
            </UTable>
          </template>
        </UTabs>
      </template>
    </template>
  </UDashboardPanel>
</template>
