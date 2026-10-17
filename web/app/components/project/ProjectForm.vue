<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import { ProjectRole, type Project } from '~/gen/relic/v1/project_pb'

const props = defineProps<{
  project?: Project
  taken: string[]
  // membersOnly edits the members alone, as a project admin may.
  membersOnly?: boolean
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { projects } = useRelic()
const toast = useToast()

const roles = [
  { label: 'Admin', value: ProjectRole.ADMIN },
  { label: 'Developer', value: ProjectRole.DEVELOPER },
  { label: 'Viewer', value: ProjectRole.VIEWER }
]
const MiB = 1048576n
const maxQuotaMiB = 8796093022207

const memberSchema = z.object({
  name: z.string().min(1, 'A user or group name'),
  group: z.boolean(),
  role: z.enum(ProjectRole)
})
const schema = z.object({
  key: z.string().regex(/^[a-z][a-z0-9]{1,31}$/, '2 to 32 lower-case letters and digits, starting with a letter')
    .refine(k => !!props.project || !props.taken.includes(k), 'A project with this key exists'),
  name: z.string(),
  description: z.string(),
  quotaMiB: z.number().int().min(0).max(maxQuotaMiB, 'At most 8 EiB'),
  members: z.array(memberSchema)
})
type Schema = z.output<typeof schema>

const p = props.project
const state = reactive<Schema>({
  key: p?.key ?? '',
  name: p?.name ?? '',
  description: p?.description ?? '',
  quotaMiB: sizeInUnits(p?.storageQuotaBytes ?? 0n, MiB),
  members: p?.members.map(m => ({ name: m.name, group: m.group, role: m.role })) ?? []
})
const saving = ref(false)

function addMember() {
  state.members.push({ name: '', group: false, role: ProjectRole.DEVELOPER })
}

const memberKey = (m: { name: string, group: boolean }) => `${m.group ? 'group' : 'user'}:${m.name}`

async function saveMemberChanges(key: string, members: Schema['members']) {
  const before = new Map((props.project?.members ?? []).map(m => [memberKey(m), m.role]))
  const after = new Set(members.map(memberKey))
  for (const m of props.project?.members ?? []) {
    if (!after.has(memberKey(m))) {
      await projects.removeProjectMember({ key, name: m.name, group: m.group })
    }
  }
  for (const m of members) {
    if (before.get(memberKey(m)) !== m.role) {
      await projects.addProjectMember({ key, member: m })
    }
  }
}

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  saving.value = true
  try {
    if (props.membersOnly) {
      await saveMemberChanges(d.key, d.members)
    } else {
      await projects.putProject({
        project: {
          key: d.key,
          name: d.name,
          description: d.description,
          storageQuotaBytes: sizeInBytes(d.quotaMiB, MiB, props.project?.storageQuotaBytes),
          members: d.members
        }
      })
    }
    toast.add({ title: `Project ${d.key} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the project', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="project ? (membersOnly ? `Members of ${project.key}` : `Edit project ${project.key}`) : 'New project'"
    description="Members get their role's actions on the repositories whose project is this key."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="project-form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <template v-if="!membersOnly">
          <UFormField name="key" label="Key" required>
            <UInput v-model="state.key" :disabled="!!project" class="w-full font-mono" />
          </UFormField>
          <UFormField name="name" label="Name">
            <UInput v-model="state.name" class="w-full" />
          </UFormField>
          <UFormField name="description" label="Description">
            <UInput v-model="state.description" class="w-full" />
          </UFormField>
          <UFormField name="quotaMiB" label="Storage quota (MiB)" description="0 for no quota.">
            <UInputNumber
              v-model="state.quotaMiB"
              :min="0"
              :max="maxQuotaMiB"
              class="w-full"
            />
          </UFormField>
        </template>

        <USeparator label="Members" />
        <p class="text-sm text-muted">
          Admins and developers read, deploy, annotate and delete in the project's repositories; viewers read. Admins
          also manage the members and the project's repositories.
        </p>
        <div v-for="(m, i) in state.members" :key="i" class="flex items-start gap-2">
          <UFormField :name="`members.${i}.name`" class="flex-1">
            <UInput
              v-model="m.name"
              :placeholder="m.group ? 'Group' : 'User'"
              :aria-label="`Member ${i + 1}`"
              class="w-full"
            />
          </UFormField>
          <USwitch v-model="m.group" label="Group" class="mt-1.5" />
          <USelect
            v-model="m.role"
            :items="roles"
            :aria-label="`Role of member ${i + 1}`"
            class="w-32"
          />
          <UButton
            icon="i-lucide-x"
            color="neutral"
            variant="ghost"
            :aria-label="`Remove member ${i + 1}`"
            @click="state.members.splice(i, 1)"
          />
        </div>
        <UButton
          icon="i-lucide-plus"
          color="neutral"
          variant="outline"
          label="Add member"
          @click="addMember()"
        />
      </UForm>
    </template>

    <template #footer>
      <UButton
        label="Cancel"
        color="neutral"
        variant="outline"
        @click="emit('close', false)"
      />
      <UButton
        type="submit"
        form="project-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
