<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import type { ImageInfo } from '~/utils/view-models'

const props = defineProps<{ repo: string, path: string }>()
const { packages } = useRelic()
const copy = useCopy()

const { data: image, status, error, refresh } = useAsyncData(
  () => `docker:${props.repo}/${props.path}`,
  async () => imageInfo(await packages.getContainerImage({ repository: props.repo, path: props.path })),
  { watch: [() => props.repo, () => props.path] }
)

type Layer = ImageInfo['layers'][number]
type Platform = NonNullable<ImageInfo['platforms']>[number]

const labels = computed(() => Object.entries(image.value?.labels ?? {}).map(([key, value]) => ({ key, value })))
const short = (digest: string) => digest.replace(/^sha256:/, '').slice(0, 12)

const layerColumns: TableColumn<Layer>[] = [
  { id: 'index', header: '#' },
  { accessorKey: 'instruction', header: 'Instruction' },
  { accessorKey: 'size', header: 'Size' },
  { accessorKey: 'digest', header: 'Digest' }
]
const platformColumns: TableColumn<Platform>[] = [
  { id: 'platform', header: 'Platform' },
  { accessorKey: 'size', header: 'Manifest size' },
  { accessorKey: 'digest', header: 'Digest' }
]
</script>

<template>
  <div class="flex flex-col gap-6 py-4">
    <LoadError
      v-if="error"
      :error="error"
      what="the image"
      @retry="refresh()"
    />
    <USkeleton v-else-if="status === 'pending' && !image" class="h-48" />

    <template v-else-if="image">
      <dl class="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-[max-content_1fr]">
        <dt class="text-sm text-muted">
          Digest
        </dt>
        <dd class="flex items-start gap-1 font-mono text-sm break-all text-default">
          <span>{{ image.digest }}</span>
          <UButton
            icon="i-lucide-copy"
            color="neutral"
            variant="ghost"
            size="xs"
            aria-label="Copy digest"
            class="-my-1"
            @click="copy(image.digest, 'Digest copied')"
          />
        </dd>
        <template v-if="image.platform">
          <dt class="text-sm text-muted">
            Platform
          </dt>
          <dd class="text-sm text-default">
            {{ image.platform }}
          </dd>
        </template>
        <template v-if="!image.platforms">
          <dt class="text-sm text-muted">
            Total size
          </dt>
          <dd class="text-sm text-default">
            {{ formatBytes(image.totalSize) }} in {{ pluralize(image.layers.length, 'layer') }}
          </dd>
        </template>
        <template v-if="image.created">
          <dt class="text-sm text-muted">
            Built
          </dt>
          <dd class="text-sm text-default">
            {{ formatDate(image.created) }}
          </dd>
        </template>
        <dt class="text-sm text-muted">
          Media type
        </dt>
        <dd class="font-mono text-xs text-default">
          {{ image.mediaType }}
        </dd>
      </dl>

      <div v-if="image.platforms" class="space-y-2">
        <h3 class="text-sm font-medium text-highlighted">
          Platforms
        </h3>
        <UTable :data="image.platforms" :columns="platformColumns">
          <template #platform-cell="{ row }">
            {{ row.original.os }}/{{ row.original.architecture }}<span v-if="row.original.variant">/{{ row.original.variant }}</span>
          </template>
          <template #size-cell="{ row }">
            {{ formatBytes(row.original.size) }}
          </template>
          <template #digest-cell="{ row }">
            <span class="font-mono text-xs">{{ short(row.original.digest) }}</span>
          </template>
        </UTable>
      </div>

      <div v-else class="space-y-2">
        <h3 class="text-sm font-medium text-highlighted">
          Layers
        </h3>
        <UTable :data="image.layers" :columns="layerColumns">
          <template #index-cell="{ row }">
            <span class="text-muted">{{ row.index + 1 }}</span>
          </template>
          <template #instruction-cell="{ row }">
            <span class="line-clamp-2 max-w-xl font-mono text-xs whitespace-normal break-all">{{ row.original.instruction || '—' }}</span>
          </template>
          <template #size-cell="{ row }">
            {{ formatBytes(row.original.size) }}
          </template>
          <template #digest-cell="{ row }">
            <span class="font-mono text-xs">{{ short(row.original.digest) }}</span>
          </template>
        </UTable>
      </div>

      <div v-if="labels.length" class="space-y-2">
        <h3 class="text-sm font-medium text-highlighted">
          Labels
        </h3>
        <UTable :data="labels" :columns="[{ accessorKey: 'key', header: 'Label' }, { accessorKey: 'value', header: 'Value' }]">
          <template #value-cell="{ row }">
            <span class="break-all whitespace-normal">{{ row.original.value }}</span>
          </template>
        </UTable>
      </div>
    </template>
  </div>
</template>
