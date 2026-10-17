<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { GroupView, PermissionAction, PermissionView, RepositoryView, UserView } from '~/utils/view-models'
import { nonRepositoryRules, repositoryRule } from '~/utils/mappers'

const props = defineProps<{
  permission?: PermissionView
  repositories: RepositoryView[]
  users: UserView[]
  groups: GroupView[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { security } = useRelic()
const toast = useToast()

const actionItems: { label: string, value: PermissionAction }[] = [
  { label: 'Read', value: 'read' },
  { label: 'Annotate', value: 'annotate' },
  { label: 'Deploy', value: 'write' },
  { label: 'Delete', value: 'delete' },
  { label: 'Manage', value: 'manage' }
]
const repositoryItems = computed(() => [
  { label: 'All repositories', value: '@all' },
  { label: 'All local repositories', value: '@local' },
  { label: 'All remote repositories', value: '@remote' },
  ...props.repositories.map(r => ({ label: r.name, value: r.name }))
])

const grant = z.object({
  principal: z.string().min(1, 'Choose a user or group'),
  actions: z.array(z.enum(['read', 'annotate', 'write', 'delete', 'manage', 'promote'])).min(1, 'Grant at least one action')
})
const grants = z.array(grant).superRefine((rows, ctx) => rows.forEach((row, i) => {
  if (row.principal && rows.findIndex(other => other.principal === row.principal) < i) {
    ctx.addIssue({ code: 'custom', message: `${row.principal} is already granted above`, path: [i, 'principal'] })
  }
}))
const schema = z.object({
  name: z.string().regex(/^[\w.-]{1,64}$/, 'Letters, digits, dot, dash and underscore only'),
  selector: z.array(z.string()).min(1, 'Choose at least one repository'),
  include: z.array(z.string()),
  exclude: z.array(z.string()),
  users: grants,
  groups: grants
})
type Schema = z.output<typeof schema>

const toRows = (m?: Record<string, PermissionAction[]>) => Object.entries(m ?? {}).map(([principal, actions]) => ({ principal, actions: [...actions] }))
const toMap = (rows: Schema['users']) => Object.fromEntries(rows.map(r => [r.principal, r.actions]))

const state = reactive<Schema>({
  name: props.permission?.name ?? '',
  selector: [...(props.permission?.selector ?? [])],
  include: [...(props.permission?.include ?? ['**'])],
  exclude: [...(props.permission?.exclude ?? [])],
  users: toRows(props.permission?.users),
  groups: toRows(props.permission?.groups)
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  const { name, selector, include, exclude, users, groups } = event.data
  try {
    if (!props.permission && await exists(() => security.getPermission({ name }))) {
      throw new Error(`permission ${name} already exists`)
    }
    await security.putPermission({
      permission: {
        name,
        rules: [repositoryRule(selector, include, exclude, toMap(users), toMap(groups)), ...nonRepositoryRules(props.permission?.source)]
      }
    })
    toast.add({ title: `Permission ${event.data.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the permission', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="permission ? `Edit ${permission.name}` : 'New permission'"
    description="Who may do what in which repositories."
    :close="{ onClick: () => emit('close', false) }"
    :ui="{ content: 'max-w-2xl' }"
  >
    <template #body>
      <UForm
        id="permission-form"
        :schema="schema"
        :state="state"
        class="space-y-6"
        @submit="onSubmit"
      >
        <UFormField name="name" label="Name" required>
          <UInput v-model="state.name" :disabled="!!permission" class="w-full" />
        </UFormField>
        <UFormField name="selector" label="Repositories" required>
          <USelectMenu
            v-model="state.selector"
            :items="repositoryItems"
            value-key="value"
            multiple
            class="w-full"
          />
        </UFormField>
        <UFormField name="include" label="Include patterns" description="Ant-style, for example org/** or *.jar. Press Enter to add.">
          <UInputTags v-model="state.include" add-on-blur class="w-full" />
        </UFormField>
        <UFormField name="exclude" label="Exclude patterns" description="Paths excluded even when included.">
          <UInputTags v-model="state.exclude" add-on-blur class="w-full" />
        </UFormField>

        <USeparator />

        <div v-for="kind in (['users', 'groups'] as const)" :key="kind" class="space-y-3">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-medium text-highlighted">
              {{ kind === 'users' ? 'Users' : 'Groups' }}
            </h3>
            <UButton
              :label="kind === 'users' ? 'Add user' : 'Add group'"
              icon="i-lucide-plus"
              color="neutral"
              variant="outline"
              size="xs"
              @click="state[kind].push({ principal: '', actions: ['read'] })"
            />
          </div>
          <p v-if="!state[kind].length" class="text-sm text-muted">
            None.
          </p>
          <div
            v-for="(row, i) in state[kind]"
            :key="i"
            class="space-y-3 rounded-md border border-default p-3"
          >
            <div class="flex items-start gap-2">
              <UFormField :name="`${kind}.${i}.principal`" class="flex-1">
                <USelectMenu
                  v-model="row.principal"
                  :items="kind === 'users' ? users.map(u => u.name) : groups.map(g => g.name)"
                  :placeholder="kind === 'users' ? 'User' : 'Group'"
                  class="w-full"
                />
              </UFormField>
              <UButton
                icon="i-lucide-x"
                color="neutral"
                variant="ghost"
                :aria-label="`Remove ${row.principal || 'row'}`"
                @click="state[kind].splice(i, 1)"
              />
            </div>
            <UFormField :name="`${kind}.${i}.actions`">
              <UCheckboxGroup
                v-model="row.actions"
                :items="actionItems"
                orientation="horizontal"
              />
            </UFormField>
          </div>
        </div>

        <UAlert
          v-if="permission?.nonRepositoryRules.length"
          icon="i-lucide-info"
          color="neutral"
          variant="subtle"
          :title="`This permission also has ${permission.nonRepositoryRules.join(' and ')} rules`"
          description="They are kept as they are; manage them with the relic CLI or the Relic API."
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
        form="permission-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
