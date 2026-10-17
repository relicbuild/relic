<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { AccessToken } from '~/gen/relic/v1/security_pb'
import { ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { security } = useRelic()
const toast = useToast()
const confirm = useOverlay().create(ConfirmModal)

const { data: tokens, status, error, refresh } = await useAsyncData('admin:tokens', async () => (await security.listTokens({})).tokens)

async function revoke(t: AccessToken) {
  if (!(await confirm.open({ title: `Revoke ${t.username}'s token ${t.description || t.id}?`, description: 'Clients using it are refused from now on.', confirmLabel: 'Revoke' }).result)) {
    return
  }
  try {
    await security.revokeToken({ id: t.id })
    toast.add({ title: 'Token revoked', color: 'success', icon: 'i-lucide-check' })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not revoke the token', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const expired = (t: AccessToken) => !!t.expires && new Date(toIsoDate(t.expires)) < new Date()

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const user = ref('all')
const state = ref('active')
watch([search, user, state], () => {
  page.value = 1
})
const all = computed<AccessToken[]>(() => tokens.value ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(t =>
    (!q || t.description.toLowerCase().includes(q) || t.username.toLowerCase().includes(q) || t.id.includes(q))
    && (user.value === 'all' || t.username === user.value)
    && (state.value === 'all' || expired(t) === (state.value === 'expired')))
})
const active = computed(() => [search.value.trim() !== '', user.value !== 'all', state.value !== 'active'].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  user.value = 'all'
  state.value = 'active'
}
const userItems = computed(() => [
  { label: 'All users', value: 'all' },
  ...[...new Set(all.value.map(t => t.username))].sort().map(u => ({ label: u, value: u }))
])
const stateItems = [
  { label: 'Active', value: 'active' },
  { label: 'Expired', value: 'expired' },
  { label: 'Active or expired', value: 'all' }
]

const columns: TableColumn<AccessToken>[] = [
  { accessorKey: 'username', header: 'User' },
  { accessorKey: 'description', header: 'Description' },
  { id: 'created', header: 'Created' },
  { id: 'expires', header: 'Expires' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-tokens" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Access tokens">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="tokens" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'token') }}</span>
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
          placeholder="User, description or ID…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelectMenu
          v-model="user"
          aria-label="Filter by user"
          :items="userItems"
          value-key="value"
          icon="i-lucide-user"
          size="sm"
          class="w-44"
        />
        <USelect
          v-model="state"
          aria-label="Filter by state"
          :items="stateItems"
          size="sm"
          class="w-44"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="access tokens"
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
        <template #username-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.username }}</span>
        </template>
        <template #description-cell="{ row }">
          {{ row.original.description || row.original.id }}
        </template>
        <template #created-cell="{ row }">
          {{ formatDate(toIsoDate(row.original.created)) }}
        </template>
        <template #expires-cell="{ row }">
          <UBadge
            v-if="expired(row.original)"
            color="neutral"
            variant="subtle"
            label="Expired"
          />
          <span v-else>{{ row.original.expires ? formatDate(toIsoDate(row.original.expires)) : 'Never' }}</span>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end">
            <UButton
              icon="i-lucide-ban"
              color="error"
              variant="ghost"
              :aria-label="`Revoke ${row.original.description || row.original.id}`"
              @click="revoke(row.original)"
            />
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-key-round"
            :title="active ? 'No token matches these filters' : 'No access tokens'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="tokens"
      />
    </template>
  </UDashboardPanel>
</template>
