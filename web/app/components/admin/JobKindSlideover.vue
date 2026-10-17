<script setup lang="ts">
import type { BackgroundJob } from '~/gen/relic/v1/system_pb'

const props = defineProps<{ kind: string }>()
const emit = defineEmits<{ close: [] }>()
const { system } = useRelic()

const state = ref('all')
const stateItems = [
  { label: 'Any state', value: 'all' },
  { label: 'Failed', value: 'discarded' },
  { label: 'Retrying', value: 'retryable' },
  { label: 'Running', value: 'running' },
  { label: 'Waiting', value: 'available' },
  { label: 'Done', value: 'completed' }
]

// Not awaited: overlays are mounted without a Suspense boundary.
const { data: jobs, status, error, refresh } = useAsyncData(
  () => `system:jobs:${props.kind}:${state.value}`,
  async () => (await system.listBackgroundJobs({ kind: props.kind, state: state.value === 'all' ? '' : state.value, limit: 100 })).jobs,
  { watch: [state] }
)

const stateColor: Record<string, 'neutral' | 'info' | 'warning' | 'error' | 'success'> = {
  available: 'neutral', scheduled: 'neutral', pending: 'neutral', running: 'info', retryable: 'warning', discarded: 'error', cancelled: 'neutral', completed: 'success'
}
const stateLabel: Record<string, string> = {
  available: 'Waiting', scheduled: 'Scheduled', pending: 'Pending', running: 'Running', retryable: 'Retrying', discarded: 'Failed', cancelled: 'Cancelled', completed: 'Done'
}
function prettyJSON(text: string) {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}
const when = (j: BackgroundJob) => formatDate(toIsoDate(j.finalized ?? j.attempted ?? j.scheduled))
</script>

<template>
  <USlideover
    :title="kind"
    description="Recent jobs, newest first. Finished jobs are kept for 24 hours."
    :close="{ onClick: () => emit('close') }"
    :ui="{ content: 'max-w-3xl' }"
  >
    <template #body>
      <div class="mb-4 flex items-center gap-2">
        <USelect
          v-model="state"
          aria-label="Filter by state"
          :items="stateItems"
          size="sm"
          class="w-40"
        />
        <UButton
          icon="i-lucide-refresh-cw"
          color="neutral"
          variant="ghost"
          size="sm"
          aria-label="Refresh"
          :loading="status === 'pending'"
          @click="refresh()"
        />
      </div>
      <LoadError
        :error="error"
        what="the jobs"
        class="mb-4"
        @retry="refresh()"
      />
      <div v-if="status === 'pending' && !jobs" class="space-y-3">
        <USkeleton v-for="i in 3" :key="i" class="h-20" />
      </div>
      <UEmpty
        v-else-if="!jobs?.length"
        variant="naked"
        icon="i-lucide-list-checks"
        title="No jobs"
        :description="state === 'all' ? 'None in the last 24 hours.' : 'None in this state.'"
      />
      <div v-else class="space-y-3">
        <UCard v-for="j in jobs" :key="String(j.id)" variant="subtle">
          <div class="flex flex-wrap items-center gap-2">
            <UBadge :color="stateColor[j.state] ?? 'neutral'" variant="subtle" :label="stateLabel[j.state] ?? j.state" />
            <span class="font-mono text-xs text-muted">#{{ j.id }}</span>
            <span class="text-xs text-muted">attempt {{ j.attempt }} of {{ j.maxAttempts }}</span>
            <span class="ms-auto text-xs text-muted">{{ when(j) }}</span>
          </div>
          <dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
            <dt class="text-muted">
              Queue
            </dt>
            <dd class="font-mono">
              {{ j.queue }}
            </dd>
            <template v-if="j.worker">
              <dt class="text-muted">
                Worker
              </dt>
              <dd class="font-mono break-all">
                {{ j.worker }}
              </dd>
            </template>
            <dt class="text-muted">
              Arguments
            </dt>
            <dd class="font-mono break-all">
              {{ j.args }}
            </dd>
          </dl>
          <div v-if="j.output" class="mt-3">
            <div class="text-xs text-muted">
              Output
            </div>
            <pre class="mt-1 rounded-md bg-elevated p-2 font-mono text-xs whitespace-pre-wrap break-words">{{ prettyJSON(j.output) }}</pre>
          </div>
          <div v-if="j.errors.length" class="mt-3 space-y-2">
            <div v-for="e in j.errors" :key="e.attempt" class="rounded-md bg-elevated p-2">
              <div class="text-xs text-muted">
                Attempt {{ e.attempt }} · {{ formatDate(toIsoDate(e.at)) }}
              </div>
              <pre class="mt-1 font-mono text-xs whitespace-pre-wrap break-words text-error">{{ e.error }}</pre>
            </div>
          </div>
        </UCard>
      </div>
    </template>
  </USlideover>
</template>
