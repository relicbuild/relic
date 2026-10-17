<script setup lang="ts">
import type { TabsItem } from '@nuxt/ui'

const props = defineProps<{ repo: string, path: string }>()
const { packages } = useRelic()
const copy = useCopy()

const { data: pkg, status, error, refresh } = useAsyncData(
  () => `npm:${props.repo}/${props.path}`,
  async () => npmInfo(await packages.getNpmPackage({ repository: props.repo, path: props.path })),
  { watch: [() => props.repo, () => props.path] }
)

const kinds = [
  { key: 'dependencies', label: 'Dependencies' },
  { key: 'devDependencies', label: 'Dev' },
  { key: 'peerDependencies', label: 'Peer' }
] as const
type Kind = typeof kinds[number]['key']
const kind = ref<Kind>('dependencies')
const kindTabs = computed<TabsItem[]>(() => kinds.map(k => ({
  label: k.label,
  value: k.key,
  badge: Object.keys(pkg.value?.[k.key] ?? {}).length || undefined
})))
const deps = computed(() => Object.entries(pkg.value?.[kind.value] ?? {}).map(([name, range]) => ({ name, range })))
const install = computed(() => pkg.value ? `npm install ${pkg.value.name}@${pkg.value.version}` : '')
</script>

<template>
  <div class="flex flex-col gap-6 py-4">
    <LoadError
      v-if="error"
      :error="error"
      what="package.json"
      @retry="refresh()"
    />
    <USkeleton v-else-if="status === 'pending' && !pkg" class="h-48" />

    <template v-else-if="pkg">
      <dl class="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-[max-content_1fr]">
        <dt class="text-sm text-muted">
          Package
        </dt>
        <dd class="text-sm text-default">
          <span class="font-medium text-highlighted">{{ pkg.name }}</span> {{ pkg.version }}
        </dd>
        <template v-if="pkg.description">
          <dt class="text-sm text-muted">
            Description
          </dt>
          <dd class="text-sm text-default">
            {{ pkg.description }}
          </dd>
        </template>
        <template v-if="pkg.license">
          <dt class="text-sm text-muted">
            License
          </dt>
          <dd class="text-sm text-default">
            {{ pkg.license }}
          </dd>
        </template>
        <template v-if="pkg.homepage || pkg.repository">
          <dt class="text-sm text-muted">
            Links
          </dt>
          <dd class="flex flex-col gap-1 text-sm break-all">
            <ULink
              v-if="pkg.homepage"
              :to="pkg.homepage"
              target="_blank"
              external
            >
              {{ pkg.homepage }}
            </ULink>
            <span v-if="pkg.repository" class="text-default">{{ pkg.repository }}</span>
          </dd>
        </template>
        <template v-if="pkg.keywords.length">
          <dt class="text-sm text-muted">
            Keywords
          </dt>
          <dd class="flex flex-wrap gap-1">
            <UBadge
              v-for="k in pkg.keywords"
              :key="k"
              color="neutral"
              variant="subtle"
              :label="k"
            />
          </dd>
        </template>
        <dt class="text-sm text-muted">
          Install
        </dt>
        <dd class="flex items-start gap-1 font-mono text-sm text-default">
          <span>{{ install }}</span>
          <UButton
            icon="i-lucide-copy"
            color="neutral"
            variant="ghost"
            size="xs"
            aria-label="Copy install command"
            class="-my-1"
            @click="copy(install, 'Install command copied')"
          />
        </dd>
      </dl>

      <div class="space-y-2">
        <UTabs
          v-model="kind"
          :items="kindTabs"
          :content="false"
          size="sm"
          class="w-fit"
        />
        <UTable :data="deps" :columns="[{ accessorKey: 'name', header: 'Name' }, { accessorKey: 'range', header: 'Version range' }]">
          <template #range-cell="{ row }">
            <span class="font-mono text-xs">{{ row.original.range }}</span>
          </template>
          <template #empty>
            <UEmpty icon="i-lucide-package" title="None" />
          </template>
        </UTable>
      </div>
    </template>
  </div>
</template>
