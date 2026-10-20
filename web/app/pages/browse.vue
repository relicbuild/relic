<script setup lang="ts">
import type { TreeItem } from '@nuxt/ui'
import { breakpointsTailwind, useBreakpoints } from '@vueuse/core'
import type { Item } from '~/gen/relic/v1/artifact_pb'

definePageMeta({ layout: 'dashboard' })

interface Node extends TreeItem {
  key: string
  repo: string
  path: string
  folder: boolean
  repoClass?: RepositoryRow['class']
  holder?: string
  loaded?: boolean
  children?: Node[]
}

const route = useRoute()
const router = useRouter()
const { artifacts } = useRelic()
const toast = useToast()

const selectedRepo = computed(() => typeof route.query.repo === 'string' ? route.query.repo : '')
const selectedPath = computed(() => typeof route.query.path === 'string' ? route.query.path : '')
const keyOf = (repo: string, path: string) => (path ? `${repo}/${path}` : repo)

const severityText = { error: 'text-error', warning: 'text-warning', info: 'text-info', neutral: 'text-muted' } as const

function placeholder(key: string): Node[] {
  return [{ label: 'Loading…', key: `${key}/…`, repo: '', path: '', folder: false, disabled: true, icon: 'i-lucide-ellipsis' }]
}

function toNode(item: Item, browsedRepository: string): Node {
  const top = severities.find(s => item.vulnerabilities[s])
  const key = keyOf(browsedRepository, item.path)
  return {
    label: item.name,
    key,
    repo: browsedRepository,
    path: item.path,
    folder: item.folder,
    holder: item.repository === browsedRepository ? undefined : item.repository,
    icon: top ? 'i-lucide-shield-alert' : fileIcon(item.name, item.folder),
    ui: top ? { linkLeadingIcon: severityText[severityColor(top)] } : undefined,
    children: item.folder ? placeholder(key) : undefined
  }
}

function repoNode(r: RepositoryRow): Node {
  return {
    label: r.name,
    key: r.name,
    repo: r.name,
    path: '',
    folder: true,
    repoClass: r.class,
    icon: formatIcon(r.format),
    ui: { linkLeadingIcon: repositoryClassColor(r.class) },
    children: placeholder(r.name)
  }
}

const { data: roots, status, error, refresh } = await useAsyncData(
  'browse:repositories',
  async () => (await listRepoRows()).sort((a, b) => a.name.localeCompare(b.name)).map(repoNode),
  { deep: true }
)

const filter = ref('')
const classFilter = ref<'all' | RepositoryRow['class']>('all')
const classItems = [
  { label: 'All classes', value: 'all' },
  { label: 'Local', value: 'local' },
  { label: 'Remote', value: 'remote' },
  { label: 'Virtual', value: 'virtual' }
]
const visible = computed(() => {
  const term = filter.value.trim().toLowerCase()
  return (roots.value ?? []).filter(n =>
    (!term || n.repo.toLowerCase().includes(term))
    && (classFilter.value === 'all' || n.repoClass === classFilter.value))
})

const expanded = ref<string[]>([])

function expandSelection() {
  if (!selectedRepo.value) {
    return
  }
  const parts = selectedPath.value.split('/').filter(Boolean)
  for (let i = 0; i < parts.length; i++) {
    const ancestor = keyOf(selectedRepo.value, parts.slice(0, i).join('/'))
    if (!expanded.value.includes(ancestor)) {
      expanded.value.push(ancestor)
    }
  }
}

watch([selectedRepo, selectedPath], expandSelection, { immediate: true })

function find(nodes: Node[] | undefined, key: string): Node | undefined {
  for (const n of nodes ?? []) {
    if (n.key === key) {
      return n
    }
    if (key.startsWith(`${n.key}/`)) {
      return find(n.children, key)
    }
  }
}

async function load(node: Node) {
  const res = await artifacts.listChildren({ repository: node.repo, path: node.path })
  node.children = res.items.map(item => toNode(item, node.repo))
  node.loaded = true
}

watch([expanded, roots], async () => {
  for (const key of expanded.value) {
    const node = find(roots.value ?? undefined, key)
    if (!node?.folder || node.loaded) {
      continue
    }
    node.loaded = true
    try {
      await load(node)
    } catch (err) {
      node.loaded = false
      toast.add({ title: 'Could not open folder', description: relicErrorMessage(err, key), color: 'error' })
    }
  }
}, { deep: true, immediate: true })

const selectedNode = computed(() => selectedRepo.value ? find(roots.value ?? undefined, keyOf(selectedRepo.value, selectedPath.value)) : undefined)

const isMobile = useBreakpoints(breakpointsTailwind).smaller('lg')
const detailsOpen = ref(false)

function onSelect(node: Node) {
  router.replace({ query: { repo: node.repo, path: node.path || undefined } })
  if (isMobile.value && !node.folder) {
    detailsOpen.value = true
  }
}

watch(selectedNode, (node) => {
  if (isMobile.value && node && !node.folder) {
    detailsOpen.value = true
  }
}, { immediate: true })

const refreshing = ref(false)
const detailKey = ref(0)

async function reload() {
  refreshing.value = true
  try {
    expanded.value = expanded.value.filter(k => !k.includes('/'))
    await refresh()
    expandSelection()
    detailKey.value++
  } finally {
    refreshing.value = false
  }
}

defineShortcuts({ r: () => reload() })

const breadcrumb = computed(() => {
  if (!selectedRepo.value) {
    return []
  }
  const parts = selectedPath.value ? selectedPath.value.split('/') : []
  return [
    { label: selectedRepo.value, icon: 'i-lucide-box', to: { query: { repo: selectedRepo.value } } },
    ...parts.map((part, i) => ({ label: part, to: { query: { repo: selectedRepo.value, path: parts.slice(0, i + 1).join('/') } } }))
  ]
})
</script>

<template>
  <UDashboardPanel
    id="browse-tree"
    :default-size="28"
    :min-size="18"
    :max-size="45"
    resizable
  >
    <template #header>
      <UDashboardNavbar title="Browse">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <UTooltip text="Refresh" :kbds="['R']">
            <UButton
              icon="i-lucide-refresh-cw"
              color="neutral"
              variant="ghost"
              aria-label="Refresh the tree"
              :loading="refreshing || status === 'pending'"
              @click="reload()"
            />
          </UTooltip>
        </template>
      </UDashboardNavbar>
      <UDashboardToolbar>
        <UInput
          v-model="filter"
          icon="i-lucide-search"
          placeholder="Filter repositories"
          class="min-w-0 flex-1"
        />
        <USelect
          v-model="classFilter"
          :items="classItems"
          aria-label="Repository class"
          class="w-36"
        />
      </UDashboardToolbar>
    </template>

    <template #body>
      <LoadError
        v-if="error"
        :error="error"
        what="the repositories"
        @retry="refresh()"
      />
      <USkeleton v-else-if="status === 'pending'" class="h-64" />
      <UTree
        v-else-if="visible.length"
        v-model:expanded="expanded"
        :model-value="selectedNode"
        :nested="false"
        :items="visible"
        :get-key="(n: Node) => n.key"
        :ui="{ linkTrailingIcon: 'hidden' }"
        @select="(_e: unknown, n: Node) => onSelect(n)"
      >
        <template #item-leading="{ item, expanded: open }">
          <ArtifactTreeLeading
            :folder="item.folder"
            :expanded="open"
            :icon="item.icon"
            :icon-class="item.ui?.linkLeadingIcon"
          />
        </template>
        <template #item-trailing="{ item }">
          <UBadge
            v-if="item.repoClass"
            :label="item.repoClass"
            color="neutral"
            variant="subtle"
            size="sm"
            class="ms-auto"
          />
          <span v-else-if="item.holder" class="ms-auto truncate text-xs text-dimmed">{{ item.holder }}</span>
        </template>
      </UTree>
      <UEmpty
        v-else
        icon="i-lucide-folder-search"
        :title="roots?.length ? 'No repository matches' : 'No repositories'"
        :description="roots?.length ? 'Change the filter to see more.' : 'Create a repository, or ask an administrator for read access to one.'"
      />
    </template>
  </UDashboardPanel>

  <USlideover
    v-if="isMobile"
    v-model:open="detailsOpen"
    :title="selectedPath.split('/').pop() || selectedRepo"
    :description="selectedPath ? `${selectedRepo}/${selectedPath}` : undefined"
  >
    <template #body>
      <ArtifactDetailPanel
        v-if="selectedRepo"
        :key="detailKey"
        :repo="selectedRepo"
        :path="selectedPath"
        @deleted="detailsOpen = false; reload()"
      />
    </template>
    <template #footer>
      <UButton
        icon="i-lucide-external-link"
        color="neutral"
        variant="outline"
        label="Open repository"
        block
        :to="{ path: `/repositories/${selectedRepo}`, query: { path: selectedPath || undefined } }"
      />
    </template>
  </USlideover>

  <UDashboardPanel v-else id="browse-item">
    <template #header>
      <UDashboardNavbar>
        <template #left>
          <UBreadcrumb v-if="breadcrumb.length" :items="breadcrumb" />
          <span v-else class="text-muted">Select a repository or an item</span>
        </template>
        <template v-if="selectedRepo" #right>
          <UButton
            icon="i-lucide-external-link"
            color="neutral"
            variant="outline"
            label="Open repository"
            :to="{ path: `/repositories/${selectedRepo}`, query: { path: selectedPath || undefined } }"
          />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <ArtifactDetailPanel
        v-if="selectedRepo"
        :key="detailKey"
        :repo="selectedRepo"
        :path="selectedPath"
        @deleted="reload()"
      />
      <UEmpty
        v-else
        icon="i-lucide-folder-tree"
        title="Browse every repository"
        description="Expand a repository to walk its folders; select anything to see its details, properties and security."
      />
    </template>
  </UDashboardPanel>
</template>
