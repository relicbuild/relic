<script setup lang="ts">
import type { ContextMenuItem, TreeItem } from '@nuxt/ui'
import { breakpointsTailwind, useBreakpoints } from '@vueuse/core'
import type { Item } from '~/gen/relic/v1/artifact_pb'
import { RepositoryClass } from '~/gen/relic/v1/repository_pb'
import { ArtifactUploadModal, ConfirmModal } from '#components'

definePageMeta({ layout: 'dashboard' })

interface Node extends TreeItem {
  path: string
  folder: boolean
  repository?: string
  loaded?: boolean
  children?: Node[]
}

const route = useRoute()
const router = useRouter()
const { artifacts, repositories } = useRelic()
const toast = useToast()
const overlay = useOverlay()
const uploader = overlay.create(ArtifactUploadModal)
const confirm = overlay.create(ConfirmModal)
const copy = useCopy()

const repo = computed(() => String(route.params.name))
const selected = computed(() => typeof route.query.path === 'string' ? route.query.path : '')

const severityText = { error: 'text-error', warning: 'text-warning', info: 'text-info', neutral: 'text-muted' } as const

const nodeIds = new Map<string, number>()
function nodeClass(path: string): string {
  if (!nodeIds.has(path)) {
    nodeIds.set(path, nodeIds.size)
  }
  return `relic-node-${nodeIds.get(path)}`
}

function toNode(it: Item): Node {
  const top = severities.find(s => it.vulnerabilities[s])
  return {
    label: it.name,
    path: it.path,
    repository: it.repository,
    folder: it.folder,
    class: nodeClass(it.path),
    icon: top ? 'i-lucide-shield-alert' : fileIcon(it.name, it.folder),
    ui: top ? { linkLeadingIcon: severityText[severityColor(top)] } : undefined,
    children: it.folder ? [{ label: 'Loading…', path: `${it.path}/…`, folder: false, disabled: true, icon: 'i-lucide-ellipsis' }] : undefined
  }
}

async function load(path: string): Promise<Node[]> {
  const res = await artifacts.listChildren({ repository: repo.value, path })
  return res.items.map(toNode)
}

const { data: roots, status, error, refresh } = await useAsyncData(
  () => `tree:${repo.value}`,
  () => load(''),
  { watch: [repo], deep: true }
)

const expanded = ref<string[]>([])
watch(repo, () => {
  expanded.value = []
})

watch(selected, (path) => {
  const parts = path.split('/').filter(Boolean)
  for (let i = 1; i < parts.length; i++) {
    const ancestor = parts.slice(0, i).join('/')
    if (!expanded.value.includes(ancestor)) {
      expanded.value.push(ancestor)
    }
  }
}, { immediate: true })

watch([expanded, roots], async () => {
  const opened = await openExpandedFolders(roots.value ?? undefined, expanded.value, load)
  for (const f of opened.failures) {
    toast.add({ title: 'Could not open folder', description: relicErrorMessage(f.error, f.path), color: 'error' })
  }
  if (opened.failures.length > 0) {
    expanded.value = opened.expanded
  }
}, { deep: true, immediate: true })

const search = ref('')
const query = useDebounced(search)
watch(repo, () => {
  search.value = ''
})
const { data: found, status: searchStatus, error: searchError } = useAsyncData(
  () => `tree-search:${repo.value}:${query.value}`,
  () => query.value.trim().length >= 2
    ? artifacts.searchItems({ repository: repo.value, name: query.value.trim(), limit: 200 }).then(r => r.items)
    : Promise.resolve(null),
  { watch: [query, repo] }
)
const searching = computed(() => query.value.trim().length >= 2)

const isMobile = useBreakpoints(breakpointsTailwind).smaller('lg')
const detailsOpen = ref(false)

function openFound(it: Item) {
  search.value = ''
  router.replace({ query: { ...route.query, path: it.path } })
  if (isMobile.value && !it.folder) {
    detailsOpen.value = true
  }
}

async function onDeleted(path: string) {
  detailsOpen.value = false
  const parent = path.split('/').slice(0, -1).join('/')
  const node = parent ? findFolderNode(roots.value ?? undefined, parent) : undefined
  if (node) {
    node.children = await load(parent)
  } else {
    await refresh()
  }
  router.replace({ query: { path: parent || undefined } })
}

const detailKey = ref(0)
const refreshing = ref(false)

async function reloadTree() {
  refreshing.value = true
  try {
    await refresh()
    detailKey.value++
  } finally {
    refreshing.value = false
  }
}

async function reloadFolder() {
  const node = selected.value ? findFolderNode(roots.value ?? undefined, selected.value) : undefined
  const folder = node?.folder ? node.path : selected.value.split('/').slice(0, -1).join('/')
  const target = folder ? findFolderNode(roots.value ?? undefined, folder) : undefined
  if (!target) {
    return reloadTree()
  }
  refreshing.value = true
  try {
    target.children = await load(folder)
    target.loaded = true
    for (const path of expanded.value) {
      const n = path.startsWith(`${folder}/`) ? findFolderNode(roots.value ?? undefined, path) : undefined
      if (n) {
        n.loaded = false
      }
    }
    detailKey.value++
  } catch (err) {
    toast.add({ title: 'Could not refresh the folder', description: relicErrorMessage(err, folder), color: 'error' })
  } finally {
    refreshing.value = false
  }
}

const refreshLabel = computed(() => {
  const node = selected.value ? findFolderNode(roots.value ?? undefined, selected.value) : undefined
  return node?.folder || !selected.value ? 'Refresh folder' : 'Refresh the containing folder'
})

const selectedNode = computed(() => (selected.value ? findFolderNode(roots.value ?? undefined, selected.value) : undefined))

function onSelect(node: Node) {
  router.replace({ query: { ...route.query, path: node.path } })
  if (isMobile.value && !node.folder) {
    detailsOpen.value = true
  }
}

watch(selectedNode, (node) => {
  if (isMobile.value && node && !node.folder) {
    detailsOpen.value = true
  }
}, { immediate: true })

defineShortcuts({ r: () => reloadFolder() })

const { data: repository } = await useAsyncData(
  () => `repository:${repo.value}`,
  () => repositories.getRepository({ name: repo.value }),
  { watch: [repo] }
)
const info = computed(() => repository.value?.repository)
const emptyHint = computed(() => {
  switch (info.value?.class) {
    case RepositoryClass.REMOTE: return 'Nothing is cached yet: artifacts appear here once a client fetches them through this repository.'
    case RepositoryClass.VIRTUAL: return `Its members (${info.value.virtual?.members.join(', ') || 'none yet'}) hold nothing you may read yet.`
      + (canUpload.value ? ` Uploads here go to ${info.value.virtual?.deployTo}.` : '')
    default: return 'Upload files here, or publish them with a package manager (Set me up).'
  }
})
const canUpload = computed(() => !!repository.value?.canUpload)

const currentFolder = computed(() => {
  const node = selected.value ? findFolderNode(roots.value ?? undefined, selected.value) : undefined
  return node?.folder ? node.path : selected.value.split('/').slice(0, -1).join('/')
})

async function openUpload(files?: File[], into = currentFolder.value) {
  const folder = await uploader.open({ repo: repo.value, folder: into, files }).result
  if (folder === false || folder === undefined) {
    return
  }
  const node = folder ? findFolderNode(roots.value ?? undefined, folder) : undefined
  if (node?.folder && node.loaded) {
    node.children = await load(folder)
    detailKey.value++
  } else {
    await reloadTree()
  }
}

const dragging = ref(0)
const draggingFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes('Files')
function onDragEnter(e: DragEvent) {
  if (canUpload.value && draggingFiles(e) && !overlay.isOpen(uploader.id)) {
    e.preventDefault()
    dragging.value++
  }
}
function onDragOver(e: DragEvent) {
  if (dragging.value && e.dataTransfer) {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }
}
function onDragLeave() {
  dragging.value = Math.max(0, dragging.value - 1)
}
function onDrop(e: DragEvent) {
  if (!dragging.value) {
    return
  }
  e.preventDefault()
  dragging.value = 0
  const files: File[] = []
  let folders = 0
  for (const item of e.dataTransfer?.items ?? []) {
    if (item.kind !== 'file') {
      continue
    }
    const file = item.getAsFile()
    if (item.webkitGetAsEntry()?.isDirectory) {
      folders++
    } else if (file) {
      files.push(file)
    }
  }
  if (folders) {
    toast.add({ title: `${pluralize(folders, 'folder')} skipped`, description: 'Only files can be dropped; drop the files inside a folder to upload them.', color: 'warning' })
  }
  if (files.length) {
    openUpload(files)
  }
}
onMounted(() => {
  window.addEventListener('dragenter', onDragEnter)
  window.addEventListener('dragover', onDragOver)
  window.addEventListener('dragleave', onDragLeave)
  window.addEventListener('drop', onDrop)
})
onBeforeUnmount(() => {
  window.removeEventListener('dragenter', onDragEnter)
  window.removeEventListener('dragover', onDragOver)
  window.removeEventListener('dragleave', onDragLeave)
  window.removeEventListener('drop', onDrop)
})

const menuTarget = ref<{ path: string, folder: boolean }>({ path: '', folder: true })
function onTreeContextMenu(e: MouseEvent) {
  const row = (e.target as HTMLElement).closest('[class*="relic-node-"]')
  const id = row ? Number(/relic-node-(\d+)/.exec(row.className)?.[1]) : NaN
  const path = [...nodeIds].find(([, v]) => v === id)?.[0]
  const node = path === undefined ? undefined : findFolderNode(roots.value ?? undefined, path)
  menuTarget.value = node ? { path: node.path, folder: node.folder } : { path: '', folder: true }
}

function openNode(path: string, folder: boolean) {
  if (folder && !expanded.value.includes(path)) {
    expanded.value.push(path)
  }
  router.replace({ query: { ...route.query, path } })
  if (isMobile.value && !folder) {
    detailsOpen.value = true
  }
}

const fileUrl = (path: string) => window.location.origin + filePath('files', repo.value, path)
const nameOf = (path: string) => path.split('/').pop() || repo.value

function download(path: string) {
  const a = document.createElement('a')
  a.href = filePath('files', repo.value, path)
  a.download = nameOf(path)
  a.click()
}

async function refreshFolder(folder: string) {
  const node = folder ? findFolderNode(roots.value ?? undefined, folder) : undefined
  if (node?.folder && node.loaded) {
    node.children = await load(folder)
  } else {
    await reloadTree()
  }
}

async function removeItem(path: string, folder: boolean) {
  const target = findFolderNode(roots.value ?? undefined, path)?.repository || repo.value
  const ok = await confirm.open({
    title: target === repo.value ? `Delete ${nameOf(path)}?` : `Delete ${nameOf(path)} from ${target}?`,
    description: folder ? 'The folder and everything in it move to the trash can, where an administrator can restore them (unless the trash can is turned off).' : 'It moves to the trash can, where an administrator can restore it (unless the trash can is turned off).'
  }).result
  if (!ok) {
    return
  }
  try {
    await artifacts.deleteItem({ repository: target, path })
    toast.add({ title: `${nameOf(path)} deleted`, color: 'success', icon: 'i-lucide-check' })
  } catch (err) {
    toast.add({ title: `Could not delete ${nameOf(path)}`, description: relicErrorMessage(err, ''), color: 'error' })
    return
  }
  const parent = path.split('/').slice(0, -1).join('/')
  await refreshFolder(parent)
  if (selected.value === path || selected.value.startsWith(`${path}/`)) {
    router.replace({ query: { path: parent || undefined } })
  }
}

const menuItems = computed<ContextMenuItem[][]>(() => {
  const { path, folder } = menuTarget.value
  const open: ContextMenuItem[] = path
    ? [{ label: folder ? 'Open' : 'Show details', icon: folder ? 'i-lucide-folder-open' : 'i-lucide-info', onSelect: () => openNode(path, folder) }]
    : []
  if (!folder) {
    open.push({ label: 'Download', icon: 'i-lucide-download', onSelect: () => download(path) })
  }
  const groups: ContextMenuItem[][] = [open]
  if (folder) {
    groups.push([
      ...(canUpload.value ? [{ label: path ? 'Upload here…' : 'Upload to the root…', icon: 'i-lucide-upload', onSelect: () => openUpload(undefined, path) }] : []),
      { label: 'Refresh', icon: 'i-lucide-refresh-cw', onSelect: () => refreshFolder(path) }
    ])
  }
  groups.push([
    { label: 'Copy path', icon: 'i-lucide-copy', onSelect: () => copy(`${repo.value}/${path}`, 'Path copied') },
    { label: 'Copy URL', icon: 'i-lucide-link', onSelect: () => copy(fileUrl(path), 'URL copied') }
  ])
  if (path && repository.value?.canDelete) {
    groups.push([{ label: 'Delete…', icon: 'i-lucide-trash', color: 'error', onSelect: () => removeItem(path, folder) }])
  }
  return groups.filter(g => g.length)
})

const breadcrumb = computed(() => {
  const parts = selected.value ? selected.value.split('/') : []
  return [
    { label: repo.value, icon: 'i-lucide-box', to: { query: {} } },
    ...parts.map((part, i) => ({ label: part, to: { query: { path: parts.slice(0, i + 1).join('/') } } }))
  ]
})
</script>

<template>
  <UDashboardPanel
    id="repository-tree"
    :default-size="25"
    :min-size="15"
    :max-size="40"
    resizable
  >
    <template #header>
      <UDashboardNavbar :title="repo">
        <template #leading>
          <UDashboardSidebarCollapse />
          <UButton
            icon="i-lucide-arrow-left"
            color="neutral"
            variant="ghost"
            to="/repositories"
            aria-label="All repositories"
          />
        </template>
        <template #right>
          <UButton
            v-if="isMobile"
            icon="i-lucide-panel-right-open"
            color="neutral"
            variant="ghost"
            :aria-label="selected ? `Show the details of ${nameOf(selected)}` : `Show the details of ${repo}`"
            @click="detailsOpen = true"
          />
          <UTooltip text="Refresh the tree">
            <UButton
              icon="i-lucide-refresh-cw"
              color="neutral"
              variant="ghost"
              aria-label="Refresh the tree"
              :loading="refreshing || status === 'pending'"
              @click="reloadTree()"
            />
          </UTooltip>
        </template>
      </UDashboardNavbar>
      <UDashboardToolbar>
        <UInput
          v-model="search"
          icon="i-lucide-search"
          placeholder="Find by name in this repository"
          :loading="searchStatus === 'pending'"
          class="w-full"
        >
          <template v-if="search" #trailing>
            <UButton
              icon="i-lucide-x"
              color="neutral"
              variant="link"
              size="sm"
              aria-label="Clear search"
              @click="search = ''"
            />
          </template>
        </UInput>
      </UDashboardToolbar>
    </template>

    <template #body>
      <LoadError
        v-if="error"
        :error="error"
        what="the repository"
        @retry="refresh()"
      />
      <div v-else-if="searching" class="flex flex-col gap-0.5">
        <p v-if="searchError" class="px-2 pb-1 text-xs text-error">
          {{ relicErrorMessage(searchError, 'The search failed.') }}
        </p>
        <p v-else class="px-2 pb-1 text-xs text-muted">
          {{ found ? pluralize(found.length, 'match') : 'Searching…' }}{{ found?.length === 200 ? ' (first 200)' : '' }}
        </p>
        <UButton
          v-for="it in found ?? []"
          :key="it.path"
          :icon="fileIcon(it.name, it.folder) ?? 'i-lucide-folder'"
          color="neutral"
          variant="ghost"
          class="justify-start text-start"
          @click="openFound(it)"
        >
          <span class="flex min-w-0 flex-col">
            <span class="truncate text-highlighted">{{ it.name }}</span>
            <span class="truncate text-xs text-muted">{{ it.path }}</span>
          </span>
        </UButton>
      </div>
      <USkeleton v-else-if="status === 'pending'" class="h-64" />
      <UContextMenu v-else-if="roots?.length" :items="menuItems">
        <div class="min-h-full" @contextmenu.capture="onTreeContextMenu">
          <UTree
            v-model:expanded="expanded"
            :model-value="selectedNode"
            :nested="false"
            :items="roots"
            :get-key="(n: Node) => n.path"
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
              <span v-if="item.repository && item.repository !== repo" class="ms-auto truncate text-xs text-dimmed">{{ item.repository }}</span>
            </template>
          </UTree>
        </div>
      </UContextMenu>
      <UEmpty
        v-else
        icon="i-lucide-folder-open"
        title="Empty repository"
        :description="emptyHint"
      />
    </template>
  </UDashboardPanel>

  <USlideover
    v-if="isMobile"
    v-model:open="detailsOpen"
    :title="nameOf(selected)"
    :description="selected ? `${repo}/${selected}` : undefined"
  >
    <template #body>
      <ArtifactDetailPanel
        :key="detailKey"
        :repo="repo"
        :path="selected"
        @deleted="onDeleted"
      />
    </template>
    <template #footer>
      <div class="flex w-full gap-2">
        <UButton
          v-if="canUpload"
          icon="i-lucide-upload"
          color="neutral"
          variant="outline"
          label="Upload"
          class="flex-1 justify-center"
          @click="openUpload()"
        />
        <UButton
          icon="i-lucide-folder-sync"
          color="neutral"
          variant="outline"
          label="Refresh"
          :aria-label="refreshLabel"
          :loading="refreshing"
          class="flex-1 justify-center"
          @click="reloadFolder()"
        />
      </div>
    </template>
  </USlideover>

  <UDashboardPanel v-else id="repository-item">
    <template #header>
      <UDashboardNavbar>
        <template #left>
          <UBreadcrumb :items="breadcrumb" />
        </template>
        <template #right>
          <UTooltip v-if="canUpload" text="Upload files, or drop them anywhere on the page">
            <UButton
              icon="i-lucide-upload"
              color="neutral"
              variant="outline"
              label="Upload"
              @click="openUpload()"
            />
          </UTooltip>
          <UTooltip :text="refreshLabel" :kbds="['R']">
            <UButton
              icon="i-lucide-folder-sync"
              color="neutral"
              variant="outline"
              label="Refresh"
              :aria-label="refreshLabel"
              :loading="refreshing"
              @click="reloadFolder()"
            />
          </UTooltip>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <ArtifactDetailPanel
        :key="detailKey"
        :repo="repo"
        :path="selected"
        @deleted="onDeleted"
      />
    </template>
  </UDashboardPanel>

  <div
    v-if="dragging"
    class="pointer-events-none fixed inset-0 z-50 flex items-center justify-center bg-default/75 p-6"
  >
    <div class="flex size-full flex-col items-center justify-center gap-3 rounded-lg border-2 border-dashed border-primary text-center">
      <UIcon name="i-lucide-upload" class="size-10 text-primary" />
      <p class="text-lg font-medium text-highlighted">
        Drop to upload
      </p>
      <p class="font-mono text-sm text-muted">
        {{ repo }}/{{ currentFolder }}
      </p>
    </div>
  </div>
</template>
