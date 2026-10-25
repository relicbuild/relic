<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'

const emit = defineEmits<{ close: [changed: boolean] }>()
const { sessions } = useRelic()
const toast = useToast()

const schema = z.object({
  currentPassword: z.string().min(1, 'Your current password'),
  newPassword: z.string().min(8, 'At least 8 characters'),
  repeated: z.string()
}).refine(v => v.newPassword === v.repeated, { message: 'The new passwords differ', path: ['repeated'] })
type Schema = z.output<typeof schema>

const state = reactive<Partial<Schema>>({ currentPassword: '', newPassword: '', repeated: '' })
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    await sessions.changePassword({ currentPassword: event.data.currentPassword, newPassword: event.data.newPassword })
    toast.add({ title: 'Password changed', color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not change the password', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <UModal
    title="Change password"
    description="Tokens and sessions stay valid. Users of LDAP or single sign-on change their password there."
    :close="{ onClick: () => emit('close', false) }"
    :ui="{ footer: 'justify-end' }"
  >
    <template #body>
      <UForm
        id="change-password-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="currentPassword" label="Current password" required>
          <UInput
            v-model="state.currentPassword"
            type="password"
            autocomplete="current-password"
            class="w-full"
          />
        </UFormField>
        <UFormField name="newPassword" label="New password" required>
          <UInput
            v-model="state.newPassword"
            type="password"
            autocomplete="new-password"
            class="w-full"
          />
        </UFormField>
        <UFormField name="repeated" label="New password again" required>
          <UInput
            v-model="state.repeated"
            type="password"
            autocomplete="new-password"
            class="w-full"
          />
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
        form="change-password-form"
        label="Change password"
        :loading="saving"
      />
    </template>
  </UModal>
</template>
