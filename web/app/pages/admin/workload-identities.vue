<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { WorkloadIdentity } from '~/gen/relic/v1/workload_identity_pb'
import { AdminWorkloadIdentityForm, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { workloadIdentities, security } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(AdminWorkloadIdentityForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, error, refresh } = await useAsyncData('admin:workload-identities', async () => {
  const [identities, users, groups] = await Promise.all([
    workloadIdentities.listWorkloadIdentities({}),
    security.listUsers({}),
    security.listGroups({})
  ])
  return { identities: identities.workloadIdentities, users: users.users.map(u => u.name), groups: groups.groups.map(g => g.name) }
})

async function edit(identity?: WorkloadIdentity) {
  const d = data.value
  if (await form.open({ identity, taken: d?.identities.map(i => i.name) ?? [], users: d?.users ?? [], groups: d?.groups ?? [] }).result) {
    await refresh()
  }
}

async function remove(identity: WorkloadIdentity) {
  if (!(await confirm.open({ title: `Delete the workload identity ${identity.name}?`, description: 'CI jobs can no longer exchange its tokens. Tokens already issued stay valid until they expire.' }).result)) {
    return
  }
  try {
    await workloadIdentities.deleteWorkloadIdentity({ name: identity.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the workload identity', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns: TableColumn<WorkloadIdentity>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'issuer', header: 'Issuer' },
  { id: 'rules', header: 'Rules' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-workload-identities">
    <template #header>
      <UDashboardNavbar title="Workload identities">
        <template #leading>
          <UDashboardSidebarCollapse />
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
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New workload identity"
            @click="edit()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        CI jobs sign in without stored secrets: they exchange the OIDC token their platform issues (GitHub Actions,
        GitLab, Buildkite, cloud workload identities) for a Relic token that lives an hour at most.
      </p>

      <LoadError :error="error" what="the workload identities" @retry="refresh()" />

      <UTable
        :data="data?.identities ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #name-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.name }}</span>
          <UBadge
            v-if="!row.original.enabled"
            class="ms-2"
            color="neutral"
            variant="subtle"
            label="Disabled"
          />
        </template>
        <template #issuer-cell="{ row }">
          <span class="font-mono text-xs">{{ row.original.issuer }}</span>
          <div class="text-xs text-muted">
            Audience <code>{{ row.original.audience }}</code>
          </div>
        </template>
        <template #rules-cell="{ row }">
          <div v-for="(rule, i) in row.original.rules" :key="i" class="text-xs">
            <code v-for="(pattern, claim) in rule.claims" :key="claim" class="me-2">{{ claim }}={{ pattern }}</code>
            <span class="text-muted">→ {{ rule.user }}</span>
            <span v-if="rule.groups.length" class="text-muted"> ({{ rule.groups.join(', ') }})</span>
          </div>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-pencil"
              color="neutral"
              variant="ghost"
              aria-label="Edit"
              @click="edit(row.original)"
            />
            <UButton
              icon="i-lucide-trash"
              color="error"
              variant="ghost"
              aria-label="Delete"
              @click="remove(row.original)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-workflow"
            title="No workload identities"
            description="Add one to let CI jobs sign in with their platform's OIDC token."
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
