<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { GroupStrategy, type LdapServer, type LdapGroupMapping } from '~/gen/relic/v1/ldap_pb'
import { AdminLdapGroupMappingForm, AdminLdapServerForm, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { ldap } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const serverForm = overlay.create(AdminLdapServerForm)
const groupMappingForm = overlay.create(AdminLdapGroupMappingForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, error, refresh } = await useAsyncData('admin:ldap', async () => {
  const [servers, groupMappings] = await Promise.all([ldap.listLdapServers({}), ldap.listLdapGroupMappings({})])
  return { servers: servers.ldapServers, groupMappings: groupMappings.ldapGroupMappings }
})

const keys = computed(() => data.value?.servers.map(s => s.name) ?? [])

async function editServer(server?: LdapServer) {
  if (await serverForm.open({ server, taken: keys.value }).result) {
    await refresh()
  }
}

async function editGroupMapping(groupMapping?: LdapGroupMapping) {
  const taken = data.value?.groupMappings.map(g => g.name) ?? []
  if (await groupMappingForm.open({ groupMapping, ldapServers: keys.value, taken }).result) {
    await refresh()
  }
}

async function remove(kind: 'server' | 'groupMapping', name: string) {
  const what = kind === 'server' ? 'LDAP server' : 'LDAP group mapping'
  const mappings = kind === 'server' ? (data.value?.groupMappings ?? []).filter(g => g.ldapServer === name).map(g => g.name) : []
  if (mappings.length) {
    toast.add({ title: `${name} is in use`, description: `The group mappings ${mappings.join(', ')} read it: delete them or point them at another server first.`, color: 'warning' })
    return
  }
  const description = kind === 'server'
    ? 'Users of this LDAP server can no longer sign in; their Relic accounts stay.'
    : 'Group memberships already synced stay until the users next sign in.'
  if (!(await confirm.open({ title: `Delete the ${what} ${name}?`, description }).result)) {
    return
  }
  try {
    await (kind === 'server' ? ldap.deleteLdapServer({ name }) : ldap.deleteLdapGroupMapping({ name }))
    await refresh()
  } catch (err) {
    toast.add({ title: `Could not delete the ${what}`, description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const serverColumns: TableColumn<LdapServer>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'url', header: 'Server' },
  { id: 'users', header: 'Users' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]

const groupMappingColumns: TableColumn<LdapGroupMapping>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'ldapServer', header: 'LDAP server' },
  { accessorKey: 'strategy', header: 'Strategy' },
  { id: 'where', header: 'Groups' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-ldap">
    <template #header>
      <UDashboardNavbar title="LDAP">
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
            label="New LDAP server"
            @click="editServer()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        Users Relic does not know sign in against the enabled LDAP servers, in name order. Local users never do, so a
        LDAP entry cannot take over a local account. When an LDAP server cannot be reached, its users are refused.
      </p>

      <LoadError :error="error" what="the LDAP settings" @retry="refresh()" />

      <UTable
        :data="data?.servers ?? []"
        :columns="serverColumns"
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
        <template #url-cell="{ row }">
          <span class="font-mono text-xs">{{ row.original.url }}</span>
          <UBadge
            v-if="row.original.startTls || row.original.url.startsWith('ldaps:')"
            class="ms-2"
            color="success"
            variant="subtle"
            icon="i-lucide-lock"
            :label="row.original.url.startsWith('ldaps:') ? 'TLS' : 'StartTLS'"
          />
        </template>
        <template #users-cell="{ row }">
          <span class="font-mono text-xs">{{ row.original.userDnPattern || row.original.search?.filter }}</span>
          <div class="text-xs text-muted">
            {{ row.original.autoCreateUser ? 'Created on first sign-in' : 'Created by an administrator' }}
          </div>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-pencil"
              color="neutral"
              variant="ghost"
              aria-label="Edit"
              @click="editServer(row.original)"
            />
            <UButton
              icon="i-lucide-trash"
              color="error"
              variant="ghost"
              aria-label="Delete"
              @click="remove('server', row.original.name)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            icon="i-lucide-book-user"
            title="No LDAP servers"
            description="Only local users can sign in."
            :actions="administers ? [{ label: 'New LDAP server', icon: 'i-lucide-plus', onClick: () => editServer() }] : []"
          />
        </template>
      </UTable>

      <div class="flex items-center justify-between gap-2 pt-4">
        <div>
          <h2 class="font-semibold text-highlighted">
            Group sync
          </h2>
          <p class="text-sm text-muted">
            On sign-in, LDAP users join the Relic groups named like their LDAP groups (create those groups
            under Groups). Memberships an administrator sets are kept.
          </p>
        </div>
        <UButton
          v-if="administers"
          icon="i-lucide-plus"
          label="New LDAP group mapping"
          color="neutral"
          variant="outline"
          :disabled="!keys.length"
          @click="editGroupMapping()"
        />
      </div>
      <UTable
        :data="data?.groupMappings ?? []"
        :columns="groupMappingColumns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #name-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.name }}</span>
        </template>
        <template #ldapServer-cell="{ row }">
          <span>{{ row.original.ldapServer }}</span>
          <UBadge
            v-if="!keys.includes(row.original.ldapServer)"
            class="ms-2"
            color="warning"
            variant="subtle"
            label="No such server"
          />
        </template>
        <template #strategy-cell="{ row }">
          <UBadge
            color="neutral"
            variant="outline"
            :label="enumName(GroupStrategy, row.original.strategy) || 'static'"
            class="capitalize"
          />
        </template>
        <template #where-cell="{ row }">
          <span v-if="row.original.strategy === GroupStrategy.HIERARCHY" class="text-xs text-muted">Units in the user's DN</span>
          <span v-else-if="row.original.strategy === GroupStrategy.DYNAMIC" class="font-mono text-xs">user's {{ row.original.memberAttribute }}</span>
          <span v-else class="font-mono text-xs">{{ row.original.baseDn }} · {{ row.original.memberAttribute }}</span>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-pencil"
              color="neutral"
              variant="ghost"
              aria-label="Edit"
              @click="editGroupMapping(row.original)"
            />
            <UButton
              icon="i-lucide-trash"
              color="error"
              variant="ghost"
              aria-label="Delete"
              @click="remove('groupMapping', row.original.name)"
            />
          </div>
        </template>
        <template #empty>
          <div class="py-6 text-center text-sm text-muted">
            No LDAP group mappings: LDAP users get only the groups an administrator gives them.
            <template v-if="!keys.length">
              Add an LDAP server first.
            </template>
          </div>
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
