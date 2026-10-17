<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { GroupView, UserView } from '~/utils/view-models'

const props = defineProps<{ user?: UserView, groups: GroupView[] }>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { security } = useRelic()
const toast = useToast()

const schema = z.object({
  name: z.string().regex(/^[\w.@-]{1,64}$/, 'Letters, digits and . _ @ - only'),
  email: z.union([z.literal(''), z.email('Invalid email')]),
  password: props.user
    ? z.union([z.literal(''), z.string().min(8, 'At least 8 characters')])
    : z.string().min(8, 'At least 8 characters'),
  admin: z.boolean(),
  viewer: z.boolean(),
  disabled: z.boolean(),
  groups: z.array(z.string())
})
type Schema = z.output<typeof schema>

const state = reactive<Partial<Schema>>({
  name: props.user?.name ?? '',
  email: props.user?.email ?? '',
  password: '',
  admin: props.user?.admin ?? false,
  viewer: props.user?.viewer ?? false,
  disabled: props.user?.disabled ?? false,
  groups: props.user?.groups ?? []
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    if (!props.user && await exists(() => security.getUser({ name: event.data.name }))) {
      throw new Error(`user ${event.data.name} already exists`)
    }
    await security.putUser({ user: event.data })
    toast.add({ title: `User ${event.data.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the user', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover :title="user ? `Edit ${user.name}` : 'New user'" :close="{ onClick: () => emit('close', false) }">
    <template #body>
      <UForm
        id="user-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="name" label="Username" required>
          <UInput v-model="state.name" :disabled="!!user" class="w-full" />
        </UFormField>
        <UFormField name="email" label="Email">
          <UInput v-model="state.email" type="email" class="w-full" />
        </UFormField>
        <UFormField
          name="password"
          label="Password"
          :required="!user"
          :hint="user ? 'Leave empty to keep' : undefined"
        >
          <UInput
            v-model="state.password"
            type="password"
            autocomplete="new-password"
            class="w-full"
          />
        </UFormField>
        <UFormField name="groups" label="Groups">
          <USelectMenu
            v-model="state.groups"
            :items="groups.map(g => g.name)"
            multiple
            class="w-full"
          />
        </UFormField>
        <UFormField name="admin">
          <USwitch v-model="state.admin" label="Administrator" />
        </UFormField>
        <UFormField name="viewer" description="Reads the administration and security pages without changing anything.">
          <USwitch v-model="state.viewer" label="Viewer" />
        </UFormField>
        <UFormField name="disabled" description="The user cannot sign in, and their tokens stop working.">
          <USwitch v-model="state.disabled" label="Disabled" />
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
        form="user-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
