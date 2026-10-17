<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { GrantView, PermissionAction } from '~/utils/view-models'

const props = defineProps<{ repo: string, path: string }>()
const { artifacts } = useRelic()

const { data: grants, status, error, refresh } = useAsyncData(
  () => `perms:${props.repo}/${props.path}`,
  async () => (await artifacts.getEffectivePermissions({ repository: props.repo, path: props.path })).grants.map(grantView),
  { watch: [() => props.repo, () => props.path] }
)

const actions: { key: PermissionAction, label: string }[] = [
  { key: 'read', label: 'Read' },
  { key: 'annotate', label: 'Annotate' },
  { key: 'write', label: 'Deploy' },
  { key: 'delete', label: 'Delete' },
  { key: 'manage', label: 'Manage' }
]

const columns: TableColumn<GrantView>[] = [
  { accessorKey: 'principal', header: 'User or group' },
  ...actions.map(a => ({ id: a.key, header: a.label })),
  { accessorKey: 'targets', header: 'Granted by' }
]
</script>

<template>
  <div class="flex flex-col gap-3 py-4">
    <LoadError
      v-if="error"
      :error="error"
      what="the permissions"
      @retry="refresh()"
    />
    <p class="text-sm text-muted">
      Administrators may do everything and are not listed.
    </p>
    <UTable :data="grants ?? []" :columns="columns" :loading="status === 'pending'">
      <template #principal-cell="{ row }">
        <span class="flex items-center gap-2">
          <UIcon :name="row.original.group ? 'i-lucide-users' : 'i-lucide-user'" class="size-4 text-muted" />
          <span class="font-medium text-highlighted">{{ row.original.principal }}</span>
        </span>
      </template>
      <template v-for="a in actions" :key="a.key" #[`${a.key}-cell`]="{ row }">
        <UIcon
          v-if="row.original.actions.includes(a.key)"
          name="i-lucide-check"
          class="size-4 text-success"
          :aria-label="a.label"
        />
      </template>
      <template #targets-cell="{ row }">
        <span class="text-xs text-muted">{{ row.original.permissions.join(', ') }}</span>
      </template>
      <template #empty>
        <UEmpty icon="i-lucide-key-round" title="Only administrators have access" />
      </template>
    </UTable>
  </div>
</template>
