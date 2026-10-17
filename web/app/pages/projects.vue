<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { ProjectRole, type Project } from '~/gen/relic/v1/project_pb'
import { ConfirmModal, ProjectForm } from '#components'

definePageMeta({ layout: 'dashboard' })

const { me, administers } = useSession()
const { projects } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(ProjectForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, error, refresh } = await useAsyncData('projects', async () => (await projects.listProjects({})).projects)

const roleLabels: Record<ProjectRole, string> = {
  [ProjectRole.UNSPECIFIED]: 'Member',
  [ProjectRole.ADMIN]: 'Admin',
  [ProjectRole.DEVELOPER]: 'Developer',
  [ProjectRole.VIEWER]: 'Viewer'
}

function leads(p: Project): boolean {
  const user = me.value
  if (!user) {
    return false
  }
  return p.members.some(m => m.role === ProjectRole.ADMIN && (m.group ? user.groups.includes(m.name) : m.name === user.username))
}

async function edit(project?: Project, membersOnly = false) {
  if (await form.open({ project, membersOnly, taken: data.value?.map(p => p.key) ?? [] }).result) {
    await refresh()
  }
}

async function remove(project: Project) {
  if (!(await confirm.open({ title: `Delete the project ${project.key}?`, description: 'Its repositories stay, and keep naming the project, but its members lose their roles.' }).result)) {
    return
  }
  try {
    await projects.deleteProject({ key: project.key })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the project', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const usage = (p: Project) => {
  const quota = toNumber(p.storageQuotaBytes)
  return quota > 0 ? Math.min(100, Math.round(toNumber(p.usedBytes) / quota * 100)) : undefined
}

const columns: TableColumn<Project>[] = [
  { accessorKey: 'key', header: 'Project' },
  { accessorKey: 'members', header: 'Members' },
  { accessorKey: 'repositories', header: 'Repositories' },
  { accessorKey: 'usedBytes', header: 'Storage' },
  { id: 'actions' }
]
</script>

<template>
  <UDashboardPanel id="projects">
    <template #header>
      <UDashboardNavbar title="Projects">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New project"
            @click="edit()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        A repository belongs to the project its settings name. Project members get their role's actions on the project's
        repositories.
      </p>

      <LoadError :error="error" what="the projects" @retry="refresh()" />

      <UTable
        :data="data ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #key-cell="{ row }">
          <span class="font-mono font-medium text-highlighted">{{ row.original.key }}</span>
          <div class="text-xs text-muted">
            {{ row.original.name }}<span v-if="row.original.description"> · {{ row.original.description }}</span>
          </div>
        </template>
        <template #members-cell="{ row }">
          <div class="flex flex-wrap gap-1">
            <UBadge
              v-for="m in row.original.members"
              :key="`${m.group}:${m.name}`"
              color="neutral"
              variant="subtle"
              :icon="m.group ? 'i-lucide-users' : 'i-lucide-user'"
              :label="`${m.name} · ${roleLabels[m.role]}`"
            />
          </div>
        </template>
        <template #repositories-cell="{ row }">
          <div class="flex flex-wrap gap-1">
            <ULink
              v-for="r in row.original.repositories"
              :key="r"
              :to="`/repositories/${r}`"
              class="text-sm"
            >
              {{ r }}
            </ULink>
          </div>
        </template>
        <template #usedBytes-cell="{ row }">
          <span class="tabular-nums text-sm">{{ formatBytes(toNumber(row.original.usedBytes)) }}</span>
          <span v-if="toNumber(row.original.storageQuotaBytes) > 0" class="text-xs text-muted"> of {{ formatBytes(toNumber(row.original.storageQuotaBytes)) }}</span>
          <UProgress
            v-if="usage(row.original) !== undefined"
            :model-value="usage(row.original)"
            size="xs"
            class="mt-1 w-32"
          />
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              v-if="!administers && leads(row.original)"
              icon="i-lucide-users"
              color="neutral"
              variant="ghost"
              label="Members"
              @click="edit(row.original, true)"
            />
            <template v-if="administers">
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
            </template>
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-folder-kanban"
            title="No projects"
            :description="administers ? 'Create a project, then name it in repositories\' settings.' : 'You are not a member of any project.'"
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
