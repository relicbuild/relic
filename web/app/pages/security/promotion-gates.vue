<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { PromotionGate } from '~/gen/relic/v1/promotion_gate_pb'
import { ConfirmModal, SecurityPromotionGateForm } from '#components'

const { administers } = useSession()

definePageMeta({ layout: 'dashboard' })

const { promotionGates } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(SecurityPromotionGateForm)
const confirm = overlay.create(ConfirmModal)

const { data, status, error, refresh } = await useAsyncData('security:promotion-gates', async () =>
  (await promotionGates.listPromotionGates({})).promotionGates)

async function edit(gate?: PromotionGate) {
  if (await form.open({ gate, taken: data.value?.map(g => g.name) ?? [] }).result) {
    await refresh()
  }
}

async function remove(gate: PromotionGate) {
  if (!(await confirm.open({ title: `Delete the gate ${gate.name}?`, description: `Promotions to ${gate.environment} no longer check it.` }).result)) {
    return
  }
  try {
    await promotionGates.deletePromotionGate({ name: gate.name })
    await refresh()
  } catch (err) {
    toast.add({ title: 'Could not delete the gate', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns: TableColumn<PromotionGate>[] = [
  { accessorKey: 'name', header: 'Gate' },
  { accessorKey: 'environment', header: 'Environment' },
  { accessorKey: 'condition', header: 'Condition' },
  ...(administers.value ? [{ id: 'actions' }] : [])
]
</script>

<template>
  <UDashboardPanel id="security-promotion-gates">
    <template #header>
      <UDashboardNavbar title="Promotion gates">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <UButton
            v-if="administers"
            icon="i-lucide-plus"
            label="New gate"
            @click="edit()"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        A release version is promoted to an environment only if every gate of that environment allows it. Each decision
        is recorded as evidence on the version.
      </p>

      <LoadError :error="error" what="the promotion gates" @retry="refresh()" />

      <UTable
        :data="data ?? []"
        :columns="columns"
        :loading="status === 'pending'"
        class="shrink-0"
      >
        <template #name-cell="{ row }">
          <span class="font-medium text-highlighted">{{ row.original.name }}</span>
          <div v-if="row.original.description" class="text-xs text-muted">
            {{ row.original.description }}
          </div>
        </template>
        <template #environment-cell="{ row }">
          <UBadge color="primary" variant="subtle" :label="row.original.environment" />
        </template>
        <template #condition-cell="{ row }">
          <code class="text-xs break-all">{{ row.original.condition }}</code>
        </template>
        <template #actions-cell="{ row }">
          <div class="flex justify-end gap-1">
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
          </div>
        </template>
        <template #empty>
          <UEmpty
            variant="naked"
            icon="i-lucide-shield-check"
            title="No promotion gates"
            description="Add a gate to require evidence, clean scans or a signature before a release reaches an environment."
          />
        </template>
      </UTable>
    </template>
  </UDashboardPanel>
</template>
