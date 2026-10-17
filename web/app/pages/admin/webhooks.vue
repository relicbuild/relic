<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { WebhookFormat, type Webhook } from '~/gen/relic/v1/webhook_pb'
import { AdminWebhookDeliveries, AdminWebhookForm, ConfirmModal } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { webhooks } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(AdminWebhookForm)
const deliveries = overlay.create(AdminWebhookDeliveries)
const confirm = overlay.create(ConfirmModal)

const { data, status, error, refresh } = await useAsyncData('admin:webhooks', async () => (await webhooks.listWebhooks({})).webhooks)

async function edit(webhook?: Webhook) {
  if (await form.open({ webhook, taken: data.value?.map(w => w.name) ?? [] }).result) {
    await refresh()
  }
}

async function remove(webhook: Webhook) {
  if (!(await confirm.open({ title: `Delete the webhook ${webhook.name}?`, description: 'Its queued deliveries are dropped.' }).result)) {
    return
  }
  try {
    await webhooks.deleteWebhook({ name: webhook.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the webhook', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function ping(webhook: Webhook) {
  try {
    await webhooks.pingWebhook({ name: webhook.name })
    toast.add({ title: `Ping queued for ${webhook.name}`, color: 'success', icon: 'i-lucide-send' })
  } catch (err) {
    toast.add({ title: 'Could not ping the webhook', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns: TableColumn<Webhook>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'url', header: 'URL' },
  { accessorKey: 'events', header: 'Events' },
  { id: 'actions' }
]
</script>

<template>
  <UDashboardPanel id="admin-webhooks">
    <template #header>
      <UDashboardNavbar title="Webhooks">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <UButton
            icon="i-lucide-refresh-cw"
            color="neutral"
            variant="outline"
            aria-label="Refresh"
            :loading="status === 'pending'"
            @click="refresh()"
          />
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New webhook"
            @click="edit()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        Relic posts events to webhooks: deploys and deletes, finished scans, blocked downloads, promotions and quarantine
        decisions. The worker delivers them, signed when the webhook has a secret, and retries failures.
      </p>

      <LoadError :error="error" what="the webhooks" @retry="refresh()" />

      <UTable
        :data="data ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #name-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.name }}</span>
          <UBadge
            v-if="!row.original.enabled"
            class="ms-2"
            color="neutral"
            variant="subtle"
            label="Disabled"
          />
          <UBadge
            v-if="row.original.format === WebhookFormat.SLACK"
            class="ms-2"
            color="neutral"
            variant="outline"
            label="Slack"
          />
          <div v-if="row.original.description" class="text-xs text-muted">
            {{ row.original.description }}
          </div>
        </template>
        <template #url-cell="{ row }">
          <span class="font-mono text-xs break-all">{{ row.original.url }}</span>
          <div class="text-xs text-muted">
            {{ row.original.hasSecret ? 'Signed' : 'Not signed' }}
          </div>
        </template>
        <template #events-cell="{ row }">
          <div class="flex flex-wrap gap-1">
            <UBadge
              v-for="e in row.original.events"
              :key="e"
              color="neutral"
              variant="subtle"
              :label="e"
            />
          </div>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
            <UButton
              icon="i-lucide-list"
              color="neutral"
              variant="ghost"
              label="Deliveries"
              @click="deliveries.open({ name: row.original.name })"
            />
            <template v-if="administers">
              <UButton
                icon="i-lucide-send"
                color="neutral"
                variant="ghost"
                aria-label="Ping"
                @click="ping(row.original)"
              />
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
            icon="i-lucide-webhook"
            title="No webhooks"
            description="Add one to notify CI, chat or other services about what happens in Relic."
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
