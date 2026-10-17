<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { UserView } from '~/utils/view-models'
import { Realm, type User } from '~/gen/relic/v1/security_pb'
import { AdminUserForm, ConfirmModal } from '#components'

definePageMeta({ layout: 'dashboard' })

const { security } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const { me, administers } = useSession()
const form = overlay.create(AdminUserForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, refresh, error } = await useAsyncData('admin:users', async () => {
  const [users, groups] = await Promise.all([security.listUsers({}).then(r => r.users), security.listGroups({}).then(r => r.groups)])
  return { users, groups }
})

async function edit(user?: UserView) {
  if (await form.open({ user, groups: data.value?.groups ?? [] }).result) {
    await refresh()
  }
}

async function remove(user: UserView) {
  if (!(await confirm.open({ title: `Delete user ${user.name}?`, description: 'Their access tokens stop working.' }).result)) {
    return
  }
  try {
    await security.deleteUser({ name: user.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the user', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const realm = ref('all')
const group = ref('all')
const adminsOnly = ref(false)
watch([search, realm, group, adminsOnly], () => {
  page.value = 1
})
const all = computed<User[]>(() => data.value?.users ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(u =>
    (!q || u.name.toLowerCase().includes(q) || u.email.toLowerCase().includes(q))
    && (realm.value === 'all' || realmName(u.realm) === realm.value)
    && (group.value === 'all' || u.groups.includes(group.value))
    && (!adminsOnly.value || u.admin))
})
const active = computed(() => [search.value.trim() !== '', realm.value !== 'all', group.value !== 'all', adminsOnly.value].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  realm.value = group.value = 'all'
  adminsOnly.value = false
}
const realms = [
  { label: 'All realms', value: 'all' },
  { label: 'Internal', value: 'internal' },
  { label: 'LDAP', value: 'ldap' },
  { label: 'Single sign-on', value: 'sso' }
]
const realmLabels: Record<string, string> = { internal: 'Internal', ldap: 'LDAP', sso: 'Single sign-on' }
function realmName(r: Realm) {
  return r === Realm.LDAP ? 'ldap' : r === Realm.SSO ? 'sso' : 'internal'
}
const groupItems = computed(() => [
  { label: 'All groups', value: 'all' },
  ...(data.value?.groups ?? []).map(g => ({ label: g.name, value: g.name }))
])

const columns: TableColumn<User>[] = [
  { accessorKey: 'name', header: 'Username' },
  { accessorKey: 'email', header: 'Email' },
  { accessorKey: 'realm', header: 'Realm' },
  { accessorKey: 'groups', header: 'Groups' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-users" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Users">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'user') }}</span>
        </template>
        <template #right>
          <UButton
            v-if="administers"
            icon="i-lucide-user-plus"
            label="New user"
            @click="edit()"
          />
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
          placeholder="Name or email…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelect
          v-model="realm"
          aria-label="Filter by realm"
          :items="realms"
          size="sm"
          class="w-36"
        />
        <USelectMenu
          v-model="group"
          aria-label="Filter by group"
          :items="groupItems"
          value-key="value"
          icon="i-lucide-users"
          size="sm"
          class="w-44"
        />
        <USwitch
          v-model="adminsOnly"
          label="Administrators only"
          size="sm"
          class="whitespace-nowrap"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="users"
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
          <span class="font-medium text-highlighted">{{ row.original.name }}</span>
          <UBadge
            v-if="row.original.admin"
            class="ms-2"
            color="primary"
            variant="subtle"
            label="Admin"
          />
          <UBadge
            v-else-if="row.original.viewer"
            class="ms-2"
            color="neutral"
            variant="subtle"
            label="Viewer"
          />
          <UBadge
            v-if="row.original.disabled"
            class="ms-2"
            color="warning"
            variant="subtle"
            label="Disabled"
          />
        </template>
        <template #realm-cell="{ row }">
          <UBadge
            color="neutral"
            variant="outline"
            size="sm"
            :label="realmLabels[realmName(row.original.realm)]"
          />
        </template>
        <template #groups-cell="{ row }">
          {{ row.original.groups.join(', ') }}
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
              v-if="row.original.name !== me?.username"
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
            icon="i-lucide-users"
            :title="active ? 'No user matches these filters' : 'No users'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="users"
      />
    </template>
  </UDashboardPanel>
</template>
