<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { BuildLinkView } from '~/utils/view-models'

defineProps<{ producedBy: BuildLinkView[], usedBy: BuildLinkView[] }>()

const columns: TableColumn<BuildLinkView>[] = [
  { accessorKey: 'name', header: 'Build' },
  { accessorKey: 'module', header: 'Module' },
  { accessorKey: 'started', header: 'Started' }
]
</script>

<template>
  <div class="flex flex-col gap-6 py-4">
    <div v-for="section in [{ title: 'Produced by', rows: producedBy, empty: 'No build published this file.' }, { title: 'Used by', rows: usedBy, empty: 'No build declared this file as a dependency.' }]" :key="section.title" class="space-y-2">
      <h3 class="text-sm font-medium text-highlighted">
        {{ section.title }}
      </h3>
      <UTable :data="section.rows" :columns="columns">
        <template #name-cell="{ row }">
          <ULink :to="buildRunLink(row.original.name, row.original.number, row.original.project)" class="font-medium">
            {{ row.original.name }} #{{ row.original.number }}
          </ULink>
          <div v-if="row.original.scopes" class="text-xs text-muted">
            {{ row.original.scopes }}
          </div>
        </template>
        <template #module-cell="{ row }">
          <span class="font-mono text-xs">{{ row.original.module }}</span>
        </template>
        <template #started-cell="{ row }">
          {{ formatDate(row.original.started) }}
        </template>
        <template #empty>
          <p class="py-4 text-center text-sm text-muted">
            {{ section.empty }}
          </p>
        </template>
      </UTable>
    </div>
  </div>
</template>
