<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { ComponentView, LicenseViolationView } from '~/utils/view-models'
import type { ComponentFilters } from '~/utils/components-filter'

const props = defineProps<{
  components: ComponentView[]
  loading: boolean
  // exportUrl is the item's SBOM endpoint; ?format= picks the format.
  exportUrl: string
}>()

const exports = computed(() => [[
  { label: 'CycloneDX JSON', icon: 'i-lucide-file-json', to: `${props.exportUrl}?format=cyclonedx`, external: true, download: true },
  { label: 'SPDX JSON', icon: 'i-lucide-file-json', to: `${props.exportUrl}?format=spdx`, external: true, download: true },
  { label: 'Syft JSON', icon: 'i-lucide-file-json', to: `${props.exportUrl}?format=syft`, external: true, download: true }
]])

const search = ref('')
const type = ref('All')
const licenses = ref<string[]>([])
const status = ref<ComponentFilters['status']>('all')

const types = computed(() => ['All', ...new Set(props.components.map(c => c.type))])
const licenseItems = computed(() => licenseFacets(props.components))

const statusItems = [
  { label: 'Any policy status', value: 'all' },
  { label: 'Violates a license policy', value: 'violating' },
  { label: 'No violation', value: 'compliant' }
]

const violating = computed(() => props.components.filter(c => c.violations.length).length)

const rows = computed(() => filterComponents(props.components, {
  search: search.value, type: type.value, licenses: licenses.value, status: status.value
}))

const page = ref(1)
const pageSize = ref(50)
const pageRows = computed(() => paginate(rows.value, page.value, pageSize.value))

watch([search, type, licenses, status], () => {
  page.value = 1
})

const active = computed(() =>
  [search.value.trim() !== '', type.value !== 'All', licenses.value.length > 0, status.value !== 'all'].filter(Boolean).length)

function clearFilters() {
  search.value = ''
  type.value = 'All'
  licenses.value = []
  status.value = 'all'
}

const violationText = (v: LicenseViolationView) =>
  `${v.license || 'No license'} violates rule ${v.rule} of policy ${v.policy} (watch ${v.watch})`
  + (v.blockDownload ? '; downloads are blocked' : '')
  + (v.failBuild ? '; builds fail' : '')

const columns: TableColumn<ComponentView>[] = [
  { accessorKey: 'name', header: 'Component' },
  { accessorKey: 'version', header: 'Version' },
  { accessorKey: 'type', header: 'Type' },
  { accessorKey: 'licenses', header: 'Licenses' },
  { accessorKey: 'locations', header: 'Found in' }
]
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col gap-3 pt-4">
    <div class="flex flex-wrap items-center gap-2">
      <UInput
        v-model="search"
        icon="i-lucide-search"
        placeholder="Component or license"
        size="sm"
        class="w-56"
      />
      <USelect
        v-model="type"
        aria-label="Filter by component type"
        :items="types"
        size="sm"
        class="w-36"
      />
      <USelectMenu
        v-model="licenses"
        :items="licenseItems"
        value-key="value"
        multiple
        icon="i-lucide-scale"
        placeholder="Any license"
        :search-input="{ placeholder: 'Filter licenses…' }"
        size="sm"
        class="w-56"
      >
        <template #item-trailing="{ item }">
          <span class="text-xs text-muted tabular-nums">{{ item.count }}</span>
        </template>
      </USelectMenu>
      <USelect
        v-model="status"
        :items="statusItems"
        icon="i-lucide-shield-check"
        size="sm"
        class="w-56"
      />
      <UButton
        v-if="active"
        color="neutral"
        variant="ghost"
        size="xs"
        icon="i-lucide-filter-x"
        :label="active === 1 ? 'Clear filter' : 'Clear filters'"
        @click="clearFilters"
      />
      <div class="ms-auto flex items-center gap-2">
        <UBadge
          v-if="violating"
          color="error"
          variant="subtle"
          icon="i-lucide-scale"
          :label="`${pluralize(violating, 'component')} violate license policies`"
        />
        <UDropdownMenu :items="exports" :content="{ align: 'end' }">
          <UButton
            icon="i-lucide-download"
            label="Export SBOM"
            color="neutral"
            variant="outline"
            size="sm"
            trailing-icon="i-lucide-chevron-down"
          />
        </UDropdownMenu>
      </div>
    </div>
    <UTable
      :data="pageRows"
      :columns="columns"
      :loading="loading"
      sticky
      :ui="denseTable"
    >
      <template #name-cell="{ row }">
        <span class="font-medium text-highlighted">{{ row.original.name }}</span>
        <div v-if="row.original.purl" class="max-w-sm truncate font-mono text-xs text-muted">
          {{ row.original.purl }}
        </div>
      </template>
      <template #type-cell="{ row }">
        <UBadge color="neutral" variant="outline" :label="row.original.type" />
      </template>
      <template #licenses-cell="{ row }">
        <div class="flex flex-wrap items-center gap-1">
          <template v-for="badge in licenseBadges(row.original)" :key="badge.license">
            <UTooltip
              v-if="badge.violations.length"
              :text="badge.violations.map(violationText).join(' · ')"
            >
              <UBadge
                color="error"
                variant="subtle"
                size="sm"
                :icon="badge.violations.some(v => v.blockDownload) ? 'i-lucide-ban' : 'i-lucide-triangle-alert'"
                :label="badge.license"
              />
            </UTooltip>
            <UBadge
              v-else
              color="neutral"
              variant="outline"
              size="sm"
              :label="badge.license"
            />
          </template>
          <span v-if="!row.original.licenses.length && !row.original.violations.length" class="text-muted">—</span>
        </div>
      </template>
      <template #locations-cell="{ row }">
        <span class="font-mono text-xs">{{ row.original.locations[0]?.path ?? '—' }}</span>
        <span v-if="row.original.locations.length > 1" class="text-xs text-muted"> +{{ row.original.locations.length - 1 }}</span>
      </template>
      <template #empty>
        <UEmpty
          icon="i-lucide-package-search"
          :title="active ? 'No component matches these filters' : 'No components'"
        />
      </template>
    </UTable>
    <TablePagination
      v-model:page="page"
      v-model:page-size="pageSize"
      :total="rows.length"
      noun="components"
      class="-mx-4 -mb-4 sm:-mx-6 sm:-mb-6"
    />
  </div>
</template>
