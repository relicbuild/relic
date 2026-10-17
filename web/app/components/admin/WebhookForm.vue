<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import { WebhookFormat, type Webhook } from '~/gen/relic/v1/webhook_pb'

const props = defineProps<{
  webhook?: Webhook
  taken: string[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { webhooks } = useRelic()
const toast = useToast()

const eventTypes = [
  { label: 'Every event', value: '*' },
  { label: 'Artifact deployed', value: 'artifact.deployed' },
  { label: 'Artifact deleted', value: 'artifact.deleted' },
  { label: 'Scan finished', value: 'scan.finished' },
  { label: 'Download blocked', value: 'download.blocked' },
  { label: 'Release promoted', value: 'release.promoted' },
  { label: 'Quarantine released', value: 'quarantine.released' },
  { label: 'Quarantine rejected', value: 'quarantine.rejected' }
]
const formats = [
  { label: 'JSON', value: WebhookFormat.JSON },
  { label: 'Slack', value: WebhookFormat.SLACK }
]

const schema = z.object({
  name: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/, 'Letters, digits, ".", "_" or "-"')
    .refine(n => !!props.webhook || !props.taken.includes(n), 'A webhook with this name exists'),
  url: z.string().regex(/^https?:\/\/\S+$/, 'An http or https URL'),
  events: z.array(z.string()).min(1, 'Choose at least one event'),
  format: z.enum(WebhookFormat),
  enabled: z.boolean(),
  description: z.string(),
  secret: z.string()
})
type Schema = z.output<typeof schema>

const w = props.webhook
const state = reactive<Schema>({
  name: w?.name ?? '',
  url: w?.url ?? '',
  events: w ? [...w.events] : ['artifact.deployed'],
  format: w?.format || WebhookFormat.JSON,
  enabled: w?.enabled ?? true,
  description: w?.description ?? '',
  secret: ''
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  saving.value = true
  try {
    if (!props.webhook && await exists(() => webhooks.getWebhook({ name: d.name }))) {
      throw new Error(`webhook ${d.name} already exists`)
    }
    await webhooks.putWebhook({ webhook: { ...d } })
    toast.add({ title: `Webhook ${d.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the webhook', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="webhook ? `Edit webhook ${webhook.name}` : 'New webhook'"
    description="Relic posts the events you choose to this URL, and retries failed deliveries."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="webhook-form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <UFormField name="name" label="Name" required>
          <UInput v-model="state.name" :disabled="!!webhook" class="w-full" />
        </UFormField>
        <UFormField name="url" label="URL" required>
          <UInput v-model="state.url" placeholder="https://hooks.example.com/relic" class="w-full font-mono" />
        </UFormField>
        <UFormField name="events" label="Events" required>
          <USelectMenu
            v-model="state.events"
            :items="eventTypes"
            value-key="value"
            multiple
            placeholder="Choose events"
            class="w-full"
          />
        </UFormField>
        <UFormField name="format" label="Format" description="Slack posts a message to a Slack incoming webhook.">
          <USelect v-model="state.format" :items="formats" class="w-full" />
        </UFormField>
        <UFormField
          name="secret"
          label="Secret"
          :description="webhook?.hasSecret ? 'Deliveries are signed. Leave empty to keep the secret.' : 'Signs each body: X-Relic-Signature is sha256= and its HMAC-SHA256.'"
        >
          <UInput
            v-model="state.secret"
            type="password"
            autocomplete="new-password"
            class="w-full"
          />
        </UFormField>
        <UFormField name="description" label="Description">
          <UInput v-model="state.description" class="w-full" />
        </UFormField>
        <UFormField name="enabled">
          <USwitch v-model="state.enabled" label="Enabled" />
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
        form="webhook-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
