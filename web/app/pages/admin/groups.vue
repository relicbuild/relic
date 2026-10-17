<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { GroupView } from '~/utils/view-models'
import { AdminGroupForm, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { security } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(AdminGroupForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, refresh, error } = await useAsyncData('admin:groups', async () => {
  const [groups, users] = await Promise.all([security.listGroups({}).then(r => r.groups), security.listUsers({}).then(r => r.users)])
  return { groups, users }
})

async function edit(group?: GroupView) {
  if (await form.open({ group, users: data.value?.users ?? [] }).result) {
    await refresh()
  }
}

async function remove(group: GroupView) {
  if (!(await confirm.open({ title: `Delete group ${group.name}?`, description: 'Members lose the permissions granted through it.' }).result)) {
    return
  }
  try {
    await security.deleteGroup({ name: group.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the group', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const page = ref(1)
const pageSize = ref(50)
const search = ref('')
const member = ref('all')
const adminOnly = ref(false)
const defaultOnly = ref(false)
watch([search, member, adminOnly, defaultOnly], () => {
  page.value = 1
})
const all = computed<GroupView[]>(() => data.value?.groups ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return all.value.filter(g =>
    (!q || g.name.toLowerCase().includes(q) || g.description.toLowerCase().includes(q))
    && (member.value === 'all' || g.members.includes(member.value))
    && (!adminOnly.value || g.admin)
    && (!defaultOnly.value || g.defaultForNewUsers))
})
const active = computed(() => [search.value.trim() !== '', member.value !== 'all', adminOnly.value, defaultOnly.value].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  member.value = 'all'
  adminOnly.value = defaultOnly.value = false
}
const memberItems = computed(() => [
  { label: 'Any member', value: 'all' },
  ...(data.value?.users ?? []).map(u => ({ label: u.name, value: u.name }))
])

const columns = computed<TableColumn<GroupView>[]>(() => [
  { accessorKey: 'name', header: 'Group' },
  { accessorKey: 'members', header: 'Members' },
  ...(all.value.some(g => g.admin || g.viewer || g.defaultForNewUsers) ? [{ id: 'flags', header: 'Flags' }] : []),
  ...(administers.value ? [{ id: 'actions' }] : [])
])
</script>

<template>
  <UDashboardPanel id="admin-groups" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Groups">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'group') }}</span>
        </template>
        <template #right>
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New group"
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
          placeholder="Name or description…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelectMenu
          v-model="member"
          aria-label="Filter by member"
          :items="memberItems"
          value-key="value"
          icon="i-lucide-user"
          size="sm"
          class="w-44"
        />
        <USwitch
          v-model="adminOnly"
          label="Administrator groups"
          size="sm"
          class="whitespace-nowrap"
        />
        <USwitch
          v-model="defaultOnly"
          label="For new users"
          size="sm"
          class="whitespace-nowrap"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="groups"
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
          <div v-if="row.original.description" class="text-xs text-muted">
            {{ row.original.description }}
          </div>
        </template>
        <template #members-cell="{ row }">
          {{ row.original.members.join(', ') }}
        </template>
        <template #flags-cell="{ row }">
          <div class="flex gap-1">
            <UBadge
              v-if="row.original.admin"
              color="primary"
              variant="subtle"
              label="Admin"
            />
            <UBadge
              v-if="row.original.viewer"
              color="neutral"
              variant="subtle"
              label="Viewer"
            />
            <UBadge
              v-if="row.original.defaultForNewUsers"
              color="neutral"
              variant="subtle"
              label="For new users"
            />
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
            icon="i-lucide-users"
            :title="active ? 'No group matches these filters' : 'No groups'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="groups"
      />
    </template>
  </UDashboardPanel>
</template>
