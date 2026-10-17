<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { IdentityProvider } from '~/gen/relic/v1/identity_provider_pb'
import { AdminIdentityProviderForm, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { identityProviders } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(AdminIdentityProviderForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, error, refresh } = await useAsyncData('admin:sso', async () => (await identityProviders.listIdentityProviders({})).identityProviders)

async function edit(provider?: IdentityProvider) {
  if (await form.open({ provider, taken: data.value?.map(p => p.name) ?? [] }).result) {
    await refresh()
  }
}

async function remove(provider: IdentityProvider) {
  if (!(await confirm.open({ title: `Delete the identity provider ${provider.name}?`, description: 'Its users can no longer sign in with it; their Relic accounts stay.' }).result)) {
    return
  }
  try {
    await identityProviders.deleteIdentityProvider({ name: provider.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the identity provider', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns: TableColumn<IdentityProvider>[] = [
  { accessorKey: 'name', header: 'Name' },
  { id: 'protocol', header: 'Protocol' },
  { id: 'accounts', header: 'Accounts' },
  { accessorKey: 'callbackPath', header: 'Callback' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-sso">
    <template #header>
      <UDashboardNavbar title="Single sign-on">
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
            label="New identity provider"
            @click="edit()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        Users sign in with an enabled identity provider from the sign-in page. A provider never signs in a local or
        LDAP account: it signs in the accounts it created, or ones SCIM created.
      </p>

      <LoadError :error="error" what="the identity providers" @retry="refresh()" />

      <UTable
        :data="data ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #name-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.displayName || row.original.name }}</span>
          <span v-if="row.original.displayName" class="ms-2 text-xs text-muted">{{ row.original.name }}</span>
          <UBadge
            v-if="!row.original.enabled"
            class="ms-2"
            color="neutral"
            variant="subtle"
            label="Disabled"
          />
        </template>
        <template #protocol-cell="{ row }">
          <UBadge color="neutral" variant="outline" :label="row.original.protocol.case === 'saml' ? 'SAML' : 'OIDC'" />
          <span v-if="row.original.protocol.case === 'oidc'" class="ms-2 font-mono text-xs">{{ row.original.protocol.value.issuer }}</span>
        </template>
        <template #accounts-cell="{ row }">
          <div class="text-xs">
            {{ row.original.autoCreateUsers ? 'Created on first sign-in' : 'Created by SCIM or an administrator' }}
          </div>
          <div v-if="row.original.groupsClaim" class="text-xs text-muted">
            Groups from <code>{{ row.original.groupsClaim }}</code>
          </div>
        </template>
        <template #callbackPath-cell="{ row }">
          <span class="font-mono text-xs">{{ row.original.callbackPath }}</span>
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
            icon="i-lucide-key-round"
            title="No identity providers"
            description="Add one to let users sign in with Entra ID, Okta, Keycloak, Google or a SAML identity provider."
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
