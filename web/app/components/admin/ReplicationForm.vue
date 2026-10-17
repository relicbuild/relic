<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import { ReplicationDirection } from '~/gen/relic/v1/replication_pb'
import type { ReplicationView, RepositoryView } from '~/utils/view-models'

const props = defineProps<{
  replication?: ReplicationView
  repositories: RepositoryView[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { replications } = useRelic()
const toast = useToast()

const schedules = ['0 * * * *', '0 2 * * *', '*/15 * * * *', '0 12 * * MON-FRI']
const directions = [
  { label: 'Push', value: ReplicationDirection.PUSH, description: 'Copy this repository\'s files to the target.' },
  { label: 'Pull', value: ReplicationDirection.PULL, description: 'Copy the target\'s files this repository lacks.' },
  { label: 'Federated', value: ReplicationDirection.FEDERATED, description: 'Keep both alike: the last write wins, and a delete wins over an older write.' }
]
const directionOf = { push: ReplicationDirection.PUSH, pull: ReplicationDirection.PULL, federated: ReplicationDirection.FEDERATED }

const schema = z.object({
  repository: z.string().min(1, 'Choose a repository'),
  direction: z.enum(ReplicationDirection),
  targetUrl: z.url({ protocol: /^https?$/, error: 'An http or https URL of the target repository' }),
  username: z.string(),
  password: z.string(),
  schedule: z.string().refine(v => v.trim().startsWith('@') || v.trim().split(/\s+/).length === 5, 'Five fields: minute, hour, day of month, month and day of week'),
  enabled: z.boolean(),
  onChange: z.boolean(),
  syncDeletes: z.boolean(),
  syncProperties: z.boolean(),
  pathPrefix: z.string()
})
type Schema = z.output<typeof schema>

const r = props.replication
const state = reactive<Schema>({
  repository: r?.repository ?? '',
  direction: r ? directionOf[r.direction] : ReplicationDirection.PUSH,
  targetUrl: r?.targetUrl ?? '',
  username: r?.username ?? '',
  password: '',
  schedule: r?.schedule ?? schedules[0]!,
  enabled: r?.enabled ?? true,
  onChange: r?.onChange ?? false,
  syncDeletes: r?.syncDeletes ?? false,
  syncProperties: r?.syncProperties ?? true,
  pathPrefix: r?.pathPrefix ?? ''
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  const { repository, ...rest } = event.data
  try {
    await replications.putReplication({ replication: { ...rest, repository, timeoutSeconds: props.replication?.timeoutSeconds ?? 0 } })
    toast.add({ title: `Replication of ${repository} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the replication', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="replication ? `Edit replication of ${replication.repository}` : 'New replication'"
    description="Copy a local repository's content to or from a repository on another server."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="replication-form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <UFormField name="repository" label="Repository" required>
          <USelectMenu
            v-model="state.repository"
            :items="repositories.map(r => r.name)"
            :disabled="!!replication"
            placeholder="Local repository"
            class="w-full"
          />
        </UFormField>
        <UFormField name="direction" label="Direction">
          <URadioGroup v-model="state.direction" :items="directions" />
        </UFormField>
        <UFormField
          name="targetUrl"
          label="Target URL"
          description="The URL of the repository on the other server, as its Set me up page shows it."
          required
        >
          <UInput v-model="state.targetUrl" class="w-full" />
        </UFormField>
        <div class="grid grid-cols-2 gap-4">
          <UFormField name="username" label="Username">
            <UInput v-model="state.username" autocomplete="off" class="w-full" />
          </UFormField>
          <UFormField name="password" label="Password or token" :hint="replication?.hasPassword ? 'Leave empty to keep' : undefined">
            <UInput
              v-model="state.password"
              type="password"
              autocomplete="new-password"
              class="w-full"
            />
          </UFormField>
        </div>
        <UFormField
          name="schedule"
          label="Schedule"
          description="A cron expression: minute, hour, day of month, month and day of week. Pick one or type your own."
          required
        >
          <UInputMenu v-model="state.schedule" :items="schedules" class="w-full font-mono" />
        </UFormField>
        <UFormField name="pathPrefix" label="Path prefix" description="Only replicate this folder. Empty replicates the whole repository.">
          <UInput v-model="state.pathPrefix" placeholder="org/example" class="w-full" />
        </UFormField>

        <USeparator />

        <UFormField name="enabled">
          <USwitch v-model="state.enabled" label="Enabled" description="Run on the schedule." />
        </UFormField>
        <UFormField name="onChange">
          <USwitch v-model="state.onChange" label="Replicate on deploy" description="Run after each change, in addition to the schedule. Federated replications always do." />
        </UFormField>
        <UFormField name="syncDeletes">
          <USwitch v-model="state.syncDeletes" label="Delete on the target" description="Remove files from the target that no longer exist here." />
        </UFormField>
        <UFormField name="syncProperties">
          <USwitch v-model="state.syncProperties" label="Copy properties" />
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
        form="replication-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
