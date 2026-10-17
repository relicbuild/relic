<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { WebhookDeliveryStatus, type WebhookDelivery } from '~/gen/relic/v1/webhook_pb'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ close: [] }>()
const { webhooks } = useRelic()

const { data, status, error, refresh } = await useAsyncData(
  `admin:webhook-deliveries:${props.name}`,
  async () => (await webhooks.listWebhookDeliveries({ webhook: props.name })).deliveries
)

const statuses: Record<WebhookDeliveryStatus, { label: string, color: 'neutral' | 'success' | 'error' }> = {
  [WebhookDeliveryStatus.UNSPECIFIED]: { label: 'Unknown', color: 'neutral' },
  [WebhookDeliveryStatus.PENDING]: { label: 'Pending', color: 'neutral' },
  [WebhookDeliveryStatus.DELIVERED]: { label: 'Delivered', color: 'success' },
  [WebhookDeliveryStatus.FAILED]: { label: 'Failed', color: 'error' }
}

const columns: TableColumn<WebhookDelivery>[] = [
  { accessorKey: 'event', header: 'Event' },
  { accessorKey: 'status', header: 'Status' },
  { accessorKey: 'created', header: 'Queued' }
]
</script>

<template>
  <USlideover
    :title="`Deliveries of ${name}`"
    description="The latest deliveries, newest first. A failed delivery is retried with backoff, up to 8 attempts in all."
    :close="{ onClick: () => emit('close') }"
    :ui="{ content: 'max-w-2xl' }"
  >
    <template #body>
      <LoadError :error="error" what="the deliveries" @retry="refresh()" />
      <UTable
        :data="data ?? []"
        :columns="columns"
        :loading="status === 'pending'"
      >
        <template #event-cell="{ row }">
          <code class="text-xs">{{ row.original.event }}</code>
        </template>
        <template #status-cell="{ row }">
          <UBadge :color="statuses[row.original.status].color" variant="subtle" :label="statuses[row.original.status].label" />
          <div class="text-xs text-muted">
            {{ pluralize(row.original.attempts, 'attempt') }}<span v-if="row.original.responseStatus">, HTTP {{ row.original.responseStatus }}</span>
          </div>
          <div v-if="row.original.error" class="text-xs text-error break-all">
            {{ row.original.error }}
          </div>
        </template>
        <template #created-cell="{ row }">
          <span class="tabular-nums text-muted">{{ formatDate(row.original.created ? toIsoDate(row.original.created) : '') }}</span>
        </template>
        <template #empty>
          <UEmpty variant="naked" icon="i-lucide-send" title="No deliveries yet" />
        </template>
      </UTable>
    </template>
  </USlideover>
</template>
