<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { PermissionView } from '~/utils/view-models'
import { permissionView } from '~/utils/mappers'
import { AdminPermissionForm, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { security } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(AdminPermissionForm)
const confirm = overlay.create(ConfirmModal)
const search = ref('')

const { data, status, refresh, error } = await useAsyncData('admin:permissions', async () => {
  const [permissions, repositories, users, groups] = await Promise.all([
    security.listPermissions({}).then(r => r.permissions.map(permissionView)),
    listRepoRows(),
    security.listUsers({}).then(r => r.users),
    security.listGroups({}).then(r => r.groups)
  ])
  return { permissions, repositories, users, groups }
})

const page = ref(1)
const pageSize = ref(50)
const repository = ref('all')
const principal = ref('all')
const buildsOnly = ref(false)
watch([search, repository, principal, buildsOnly], () => {
  page.value = 1
})
const all = computed<PermissionView[]>(() => data.value?.permissions ?? [])
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  const [kind, who] = principal.value.split(/:(.*)/s)
  return all.value.filter(p =>
    (!q || p.name.toLowerCase().includes(q) || p.selector.some(r => r.toLowerCase().includes(q)))
    && (repository.value === 'all' || covers(p.selector, repository.value))
    && (principal.value === 'all' || (kind === 'user' ? who! in p.users : who! in p.groups))
    && (!buildsOnly.value || p.nonRepositoryRules.includes('build')))
})
function covers(selector: string[], repository: string): boolean {
  if (selector.includes(repository) || selector.includes('@all')) {
    return true
  }
  const cls = data.value?.repositories.find(r => r.name === repository)?.class
  return !!cls && selector.includes(`@${cls}`)
}
const active = computed(() => [search.value.trim() !== '', repository.value !== 'all', principal.value !== 'all', buildsOnly.value].filter(Boolean).length)
function clearFilters() {
  search.value = ''
  repository.value = principal.value = 'all'
  buildsOnly.value = false
}
const repositoryItems = computed(() => [
  { label: 'All repositories', value: 'all' },
  ...[['@all', 'Every repository (@all)'], ['@local', 'Every local (@local)'], ['@remote', 'Every remote (@remote)']].map(([value, label]) => ({ label, value })),
  ...(data.value?.repositories ?? []).map(r => ({ label: r.name, value: r.name }))
])
const principalItems = computed(() => [
  { label: 'Anyone', value: 'all' },
  ...(data.value?.users ?? []).map(u => ({ label: u.name, value: `user:${u.name}`, icon: 'i-lucide-user' })),
  ...(data.value?.groups ?? []).map(g => ({ label: g.name, value: `group:${g.name}`, icon: 'i-lucide-users' }))
])

async function edit(permission?: PermissionView) {
  const result = await form.open({
    permission,
    repositories: data.value?.repositories ?? [],
    users: data.value?.users ?? [],
    groups: data.value?.groups ?? []
  }).result
  if (result) {
    await refresh()
  }
}

async function remove(permission: PermissionView) {
  if (!(await confirm.open({ title: `Delete permission ${permission.name}?`, description: 'Users and groups lose the access it grants.' }).result)) {
    return
  }
  try {
    await security.deletePermission({ name: permission.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the permission', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const selectorLabel = (s: string) => ({ '@all': 'All repositories', '@local': 'All local', '@remote': 'All remote' } as Record<string, string>)[s] ?? s

const principals = (p: PermissionView) => [
  ...Object.keys(p.users).map(u => ({ label: u, icon: 'i-lucide-user' })),
  ...Object.keys(p.groups).map(g => ({ label: g, icon: 'i-lucide-users' }))
]

const columns: TableColumn<PermissionView>[] = [
  { accessorKey: 'name', header: 'Permission' },
  { accessorKey: 'selector', header: 'Repositories' },
  { id: 'patterns', header: 'Paths' },
  { id: 'principals', header: 'Granted to' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="admin-permissions" :ui="fullBleedPanel">
    <template #header>
      <UDashboardNavbar title="Permissions">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #trailing>
          <span v-if="data" class="text-xs text-muted tabular-nums">{{ pluralize(all.length, 'permission') }}</span>
        </template>
        <template #right>
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New permission"
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
          placeholder="Name or repository…"
          size="sm"
          class="w-full sm:w-64"
        />
        <USelectMenu
          v-model="repository"
          :items="repositoryItems"
          value-key="value"
          icon="i-lucide-box"
          size="sm"
          class="w-48"
        />
        <USelectMenu
          v-model="principal"
          :items="principalItems"
          value-key="value"
          icon="i-lucide-key-round"
          size="sm"
          class="w-48"
        />
        <USwitch
          v-model="buildsOnly"
          label="Grants builds"
          size="sm"
          class="whitespace-nowrap"
        />
      </TableFilters>

      <LoadError
        :error="error"
        what="permissions"
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
            v-for="kind in row.original.nonRepositoryRules"
            :key="kind"
            class="ms-2"
            color="neutral"
            variant="subtle"
            :label="kind === 'build' ? 'Builds' : 'Releases'"
          />
        </template>
        <template #selector-cell="{ row }">
          <div class="flex flex-wrap gap-1">
            <UBadge
              v-for="r in row.original.selector"
              :key="r"
              color="neutral"
              variant="outline"
              :label="selectorLabel(r)"
            />
          </div>
        </template>
        <template #patterns-cell="{ row }">
          <div class="text-xs">
            <span class="text-default">{{ row.original.include.join(', ') || '**' }}</span>
            <div v-if="row.original.exclude.length" class="text-muted">
              except {{ row.original.exclude.join(', ') }}
            </div>
          </div>
        </template>
        <template #principals-cell="{ row }">
          <div class="flex flex-wrap gap-1">
            <UBadge
              v-for="p in principals(row.original)"
              :key="p.icon + p.label"
              :icon="p.icon"
              color="neutral"
              variant="subtle"
              :label="p.label"
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
            icon="i-lucide-key-round"
            :title="active ? 'No permission matches these filters' : 'No permissions yet'"
            :description="active ? undefined : 'Only administrators can use repositories until you grant access.'"
          />
        </template>
      </UTable>

      <TablePagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
        noun="permissions"
      />
    </template>
  </UDashboardPanel>
</template>
