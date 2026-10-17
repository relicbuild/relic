<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { RegistryEntry } from '~/gen/relic/v1/agent_registry_pb'
import { Format } from '~/gen/relic/v1/repository_pb'

definePageMeta({ layout: 'dashboard' })

const { administers } = useSession()
const { agentRegistry, repositories } = useRelic()
const toast = useToast()

const { data: repos } = await useAsyncData('agent-registry:repositories', async () =>
  (await repositories.listRepositories({})).repositories
    .filter(r => r.format === Format.MCP || r.format === Format.SKILLS)
    .map(r => ({ label: r.name, value: r.name, format: r.format })))

const repo = ref<string>(repos.value?.[0]?.value ?? '')

const { data: entries, status, error, refresh } = await useAsyncData(
  () => `agent-registry:${repo.value}`,
  async () => repo.value ? (await agentRegistry.listRegistryEntries({ repository: repo.value })).entries : [],
  { watch: [repo] }
)

const isMCP = computed(() => repos.value?.find(r => r.value === repo.value)?.format === Format.MCP)

const deciding = reactive(new Set<string>())
const entryKey = (entry: RegistryEntry) => `${entry.repository}/${entry.path}`

async function decide(entry: RegistryEntry, approve: boolean) {
  if (deciding.has(entryKey(entry))) {
    return
  }
  deciding.add(entryKey(entry))
  try {
    const target = { repository: entry.repository, path: entry.path }
    await (approve
      ? agentRegistry.approveRegistryEntry({ ...target, sha256: entry.sha256 })
      : agentRegistry.revokeRegistryApproval(target))
    toast.add({ title: `${approve ? 'Approved' : 'Revoked'} ${entry.name} ${entry.version}`, color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not change the approval', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    deciding.delete(entryKey(entry))
  }
}

const columns: TableColumn<RegistryEntry>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'version', header: 'Version' },
  { accessorKey: 'approved', header: 'Approval' },
  { accessorKey: 'path', header: 'File' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="agent-registry">
    <template #header>
      <UDashboardNavbar title="Agent registry">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <USelect
            v-model="repo"
            :items="repos ?? []"
            aria-label="Repository"
            placeholder="Choose a repository"
            class="w-48"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        MCP server definitions and agent skill bundles are served only once an administrator approves their content.
        <span v-if="isMCP">MCP clients list the approved servers at <code>/api/mcp/{{ repo }}/v0/servers</code>.</span>
      </p>

      <LoadError :error="error" what="the registry" @retry="refresh()" />

      <UTable
        :data="entries ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #name-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.name }}</span>
          <div v-if="row.original.description" class="text-xs text-muted">
            {{ row.original.description }}
          </div>
        </template>
        <template #version-cell="{ row }">
          <span class="font-mono text-sm">{{ row.original.version || '—' }}</span>
        </template>
        <template #approved-cell="{ row }">
          <UBadge
            :color="row.original.approved ? 'success' : 'warning'"
            variant="subtle"
            :label="row.original.approved ? `Approved by ${row.original.approvedBy}` : 'Awaiting approval'"
          />
        </template>
        <template #path-cell="{ row }">
          <ULink :to="{ path: `/repositories/${row.original.repository}`, query: { path: row.original.path } }" class="break-all text-sm">
            {{ row.original.path }}
          </ULink>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end">
            <UButton
              v-if="!row.original.approved"
              icon="i-lucide-check"
              color="neutral"
              variant="ghost"
              label="Approve"
              :loading="deciding.has(entryKey(row.original))"
              @click="decide(row.original, true)"
            />
            <UButton
              v-else
              icon="i-lucide-x"
              color="error"
              variant="ghost"
              label="Revoke"
              :loading="deciding.has(entryKey(row.original))"
              @click="decide(row.original, false)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-plug"
            :title="repos?.length ? 'No entries' : 'No mcp or skills repositories'"
            :description="repos?.length ? 'Upload server.json files or skill bundles (zip files with a SKILL.md).' : 'Create a local repository of format mcp or skills.'"
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
