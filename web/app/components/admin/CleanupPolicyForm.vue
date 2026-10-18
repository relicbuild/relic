<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { CleanupPolicyView, RepositoryView } from '~/utils/view-models'
import { AdminCleanupPreview } from '#components'

const props = defineProps<{
  policy?: CleanupPolicyView
  repositories: RepositoryView[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { cleanup } = useRelic()
const toast = useToast()
const preview = useOverlay().create(AdminCleanupPreview)

const days = z.number().int().min(0, 'Zero or more')
const schema = z.object({
  name: z.string().trim().min(1, 'Name the policy'),
  enabled: z.boolean(),
  repositories: z.array(z.string()),
  pathPattern: z.string(),
  olderThanDays: days,
  notDownloadedDays: days,
  keepLatest: days
}).refine(p => !p.enabled || p.repositories.length > 0, {
  path: ['repositories'],
  message: 'Choose at least one repository, or disable the policy'
}).refine(p => p.olderThanDays > 0 || p.notDownloadedDays > 0 || p.keepLatest > 0, {
  path: ['olderThanDays'],
  message: 'Set at least one of older than, not downloaded for or keep latest'
})
type Schema = z.output<typeof schema>

const p = props.policy
const state = reactive<Schema>({
  name: p?.name ?? '',
  enabled: p?.enabled ?? true,
  repositories: [...(p?.repositories ?? [])],
  pathPattern: p?.pathPattern ?? '',
  olderThanDays: p?.olderThanDays ?? 0,
  notDownloadedDays: p?.notDownloadedDays ?? 0,
  keepLatest: p?.keepLatest ?? 0
})
const form = useTemplateRef('form')
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    await cleanup.putCleanupPolicy({ policy: event.data })
    toast.add({ title: `Cleanup policy ${event.data.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the cleanup policy', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}

async function openPreview() {
  try {
    await form.value?.validate({})
  } catch {
    return
  }
  preview.open({ policy: { ...state, repositories: [...state.repositories] } })
}
</script>

<template>
  <USlideover
    :title="policy ? `Edit cleanup policy ${policy.name}` : 'New cleanup policy'"
    description="Move old or unused items of local repositories to the trash every day."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="cleanup-policy-form"
        ref="form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <UFormField name="name" label="Name" required>
          <UInput v-model="state.name" :disabled="!!policy" class="w-full" />
        </UFormField>
        <UFormField name="repositories" label="Repositories" :required="state.enabled">
          <USelectMenu
            v-model="state.repositories"
            :items="repositories.map(r => r.name)"
            multiple
            placeholder="Local repositories"
            class="w-full"
          />
        </UFormField>
        <UFormField
          name="pathPattern"
          label="Path pattern"
          description="An Ant pattern: * matches within a folder, ** across folders. org/app/* selects the version folders of org/app. Empty selects everything."
        >
          <UInput v-model="state.pathPattern" placeholder="**" class="w-full font-mono" />
        </UFormField>

        <USeparator />

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <UFormField name="olderThanDays" label="Older than (days)">
            <UInputNumber v-model="state.olderThanDays" :min="0" class="w-full" />
          </UFormField>
          <UFormField name="notDownloadedDays" label="Not downloaded for (days)">
            <UInputNumber v-model="state.notDownloadedDays" :min="0" class="w-full" />
          </UFormField>
          <UFormField name="keepLatest" label="Keep latest">
            <UInputNumber v-model="state.keepLatest" :min="0" class="w-full" />
          </UFormField>
        </div>
        <p class="text-xs text-muted">
          An item is selected when it meets every criterion set; zero leaves a criterion out. Keep latest spares the newest items of each folder.
        </p>

        <USeparator />

        <UFormField name="enabled">
          <USwitch v-model="state.enabled" label="Enabled" description="Run every day." />
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
        label="Preview"
        icon="i-lucide-eye"
        color="neutral"
        variant="outline"
        @click="openPreview"
      />
      <UButton
        type="submit"
        form="cleanup-policy-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
