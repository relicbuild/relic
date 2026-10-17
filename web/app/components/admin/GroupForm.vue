<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { GroupView, UserView } from '~/utils/view-models'

const props = defineProps<{ group?: GroupView, users: UserView[] }>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { security } = useRelic()
const toast = useToast()

const schema = z.object({
  name: z.string().regex(/^[\w.-]{1,64}$/, 'Letters, digits, dot, dash and underscore only'),
  description: z.string(),
  defaultForNewUsers: z.boolean(),
  admin: z.boolean(),
  viewer: z.boolean(),
  members: z.array(z.string())
})
type Schema = z.output<typeof schema>

const state = reactive<Partial<Schema>>({
  name: props.group?.name ?? '',
  description: props.group?.description ?? '',
  defaultForNewUsers: props.group?.defaultForNewUsers ?? false,
  admin: props.group?.admin ?? false,
  viewer: props.group?.viewer ?? false,
  members: props.group?.members ?? []
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    if (!props.group && await exists(() => security.getGroup({ name: event.data.name }))) {
      throw new Error(`group ${event.data.name} already exists`)
    }
    await security.putGroup({ group: event.data })
    toast.add({ title: `Group ${event.data.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the group', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover :title="group ? `Edit ${group.name}` : 'New group'" :close="{ onClick: () => emit('close', false) }">
    <template #body>
      <UForm
        id="group-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="name" label="Name" required>
          <UInput v-model="state.name" :disabled="!!group" class="w-full" />
        </UFormField>
        <UFormField name="description" label="Description">
          <UInput v-model="state.description" class="w-full" />
        </UFormField>
        <UFormField name="members" label="Members">
          <USelectMenu
            v-model="state.members"
            :items="users.map(u => u.name)"
            multiple
            class="w-full"
          />
        </UFormField>
        <UFormField name="defaultForNewUsers" description="New users join this group.">
          <USwitch v-model="state.defaultForNewUsers" label="Add new users automatically" />
        </UFormField>
        <UFormField name="admin" description="Members are administrators.">
          <USwitch v-model="state.admin" label="Admin privileges" />
        </UFormField>
        <UFormField name="viewer" description="Members read the administration and security pages without changing anything.">
          <USwitch v-model="state.viewer" label="Viewer privileges" />
        </UFormField>
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
        form="group-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
