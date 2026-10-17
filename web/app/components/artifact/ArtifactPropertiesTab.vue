<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { ArtifactPropertyFormModal, ConfirmModal } from '#components'

const props = defineProps<{
  repo: string
  path: string
  folder: boolean
  properties: Record<string, string[]>
  canEdit: boolean
}>()
const emit = defineEmits<{ changed: [] }>()
const { artifacts } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const form = overlay.create(ArtifactPropertyFormModal)
const confirm = overlay.create(ConfirmModal)

type Row = { key: string, values: string[] }
const rows = computed<Row[]>(() => Object.entries(props.properties).map(([key, values]) => ({ key, values })))

async function edit(property?: Row) {
  if (await form.open({ repo: props.repo, path: props.path, folder: props.folder, property }).result) {
    emit('changed')
  }
}

async function remove(property: Row) {
  if (!(await confirm.open({ title: `Remove property ${property.key}?` }).result)) {
    return
  }
  try {
    await artifacts.deleteProperties({ repository: props.repo, path: props.path, keys: [property.key] })
    emit('changed')
  } catch (err) {
    toast.add({ title: 'Could not remove the property', description: relicErrorMessage(err, ''), color: 'error' })
  }
}

const columns = computed<TableColumn<Row>[]>(() => [
  { accessorKey: 'key', header: 'Property' },
  { accessorKey: 'values', header: 'Values' },
  ...(props.canEdit ? [{ id: 'actions' }] : [])
])
</script>

<template>
  <div class="flex flex-col gap-3 py-4">
    <div v-if="canEdit" class="flex justify-end">
      <UButton
        icon="i-lucide-plus"
        label="Add property"
        size="sm"
        @click="edit()"
      />
    </div>
    <UTable :data="rows" :columns="columns">
      <template #key-cell="{ row }">
        <span class="font-mono text-sm text-highlighted">{{ row.original.key }}</span>
      </template>
      <template #values-cell="{ row }">
        <div class="flex flex-wrap gap-1">
          <UBadge
            v-for="v in row.original.values"
            :key="v"
            color="neutral"
            variant="subtle"
            :label="v"
          />
        </div>
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
            aria-label="Remove"
            @click="remove(row.original)"
          />
        </div>
      </template>
      <template #empty>
        <UEmpty icon="i-lucide-tags" title="No properties" />
      </template>
    </UTable>
  </div>
</template>
