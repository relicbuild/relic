<script setup lang="ts">
import type { TabsItem, DropdownMenuItem } from '@nuxt/ui'
import { ArtifactCopyModal, ConfirmModal } from '#components'

const props = defineProps<{
  repo: string
  path: string
}>()
const emit = defineEmits<{ deleted: [path: string] }>()

const { artifacts, scans } = useRelic()
const copy = useCopy()
const { views } = useSession()
const toast = useToast()
const overlay = useOverlay()
const confirm = overlay.create(ConfirmModal)
const copyModal = overlay.create(ArtifactCopyModal)
const route = useRoute()
const router = useRouter()
const { data: artifact, status, error, refresh } = await useAsyncData(
  () => `artifact:${props.repo}/${props.path}`,
  async () => artifactView(await artifacts.getItemDetails({ repository: props.repo, path: props.path })),
  { watch: [() => props.repo, () => props.path] }
)
const holder = computed(() => artifact.value?.item.repo || props.repo)

const general = computed(() => {
  const it = artifact.value?.item
  if (!it) {
    return []
  }
  const rows: { label: string, value: string, copy?: boolean }[] = [
    { label: 'Repository', value: it.repo },
    { label: 'Path', value: it.path || '/', copy: true },
    { label: 'URL', value: useRequestURL().origin + filePath('files', it.repo, it.path), copy: true },
    { label: 'Created', value: `${formatDate(it.created)}${it.createdBy ? ` by ${it.createdBy}` : ''}` },
    { label: 'Modified', value: `${formatDate(it.modified)}${it.modifiedBy ? ` by ${it.modifiedBy}` : ''}` }
  ]
  if (!it.folder) {
    const d = artifact.value?.downloads
    rows.push({
      label: 'Downloads',
      value: d ? `${d.count}, last ${formatDate(d.last)}${d.lastDownloadedBy ? ` by ${d.lastDownloadedBy}` : ''}` : 'Never downloaded'
    })
    rows.splice(2, 0, { label: 'Size', value: formatBytes(it.size) })
    rows.push(
      { label: 'SHA-256', value: it.sha256 ?? '', copy: true },
      { label: 'SHA-1', value: it.sha1 ?? '', copy: true },
      { label: 'MD5', value: it.md5 ?? '', copy: true }
    )
  }
  return rows
})

const downloadUrl = computed(() => {
  const it = artifact.value?.item
  if (!it || it.folder) {
    return undefined
  }
  return filePath('files', it.repo, it.path)
})

const fileUrl = computed(() => general.value.find(r => r.label === 'URL')?.value ?? '')

async function remove() {
  const it = artifact.value?.item
  if (!it) {
    return
  }
  const ok = await confirm.open({
    title: `Delete ${it.name}?`,
    description: it.folder ? 'The folder and everything in it move to the trash can, where an administrator can restore them (unless the trash can is turned off).' : 'It moves to the trash can, where an administrator can restore it (unless the trash can is turned off).'
  }).result
  if (!ok) {
    return
  }
  try {
    await artifacts.deleteItem({ repository: holder.value, path: props.path })
    toast.add({ title: `${it.name} deleted`, color: 'success', icon: 'i-lucide-check' })
    emit('deleted', props.path)
  } catch (err) {
    toast.add({ title: `Could not delete ${it.name}`, description: relicErrorMessage(err, ''), color: 'error' })
  }
}

async function copyTo(move: boolean) {
  const it = artifact.value?.item
  if (it && await copyModal.open({ repo: holder.value, path: props.path, name: it.name, move }).result && move) {
    emit('deleted', props.path)
  }
}

const actions = computed<DropdownMenuItem[][]>(() => {
  const it = artifact.value?.item
  if (!it) {
    return []
  }
  const groups: DropdownMenuItem[][] = [[
    { label: 'Copy URL', icon: 'i-lucide-link', onSelect: () => copy(fileUrl.value, 'URL copied') },
    { label: 'Copy path', icon: 'i-lucide-copy', onSelect: () => copy(`${it.repo}/${it.path}`, 'Path copied') }
  ]]
  if (it.path) {
    groups.push([
      { label: 'Copy to…', icon: 'i-lucide-copy-plus', onSelect: () => copyTo(false) },
      ...(artifact.value?.canDelete ? [{ label: 'Move to…', icon: 'i-lucide-folder-input', onSelect: () => copyTo(true) }] : [])
    ])
  }
  if (artifact.value?.canDelete) {
    groups.push([{ label: 'Delete', icon: 'i-lucide-trash', color: 'error', onSelect: remove }])
  }
  return groups
})

const hasSbom = computed(() => ['done', 'partial'].includes(artifact.value?.scan.status ?? '') || (artifact.value?.scan.components ?? 0) > 0)

const gav = computed(() => {
  const it = artifact.value?.item
  const layouts = ['maven', 'gradle', 'ivy', 'sbt']
  return it && !it.folder && layouts.includes(artifact.value!.format) ? mavenCoordinates(it.path) : undefined
})

const tabs = computed<TabsItem[]>(() => {
  const items: TabsItem[] = [
    { label: 'General', icon: 'i-lucide-info', slot: 'general', value: 'general' },
    { label: 'Properties', icon: 'i-lucide-tags', slot: 'properties', value: 'properties', badge: Object.keys(artifact.value?.properties ?? {}).length || undefined }
  ]
  if (artifact.value && (!artifact.value.item.folder || artifact.value.scan.source)) {
    items.push({ label: 'Security', icon: 'i-lucide-shield', slot: 'security', value: 'security', badge: artifact.value.scan.findings.length || undefined })
  }
  if (artifact.value?.image) {
    items.push({ label: 'Docker', icon: 'i-lucide-container', slot: 'docker', value: 'docker' })
  }
  const it = artifact.value?.item
  if (it && !it.folder && artifact.value?.format === 'npm' && it.name.endsWith('.tgz')) {
    items.push({ label: 'npm', icon: 'i-lucide-package', slot: 'npm', value: 'npm' })
  }
  const type = artifact.value?.format
  if (it && !it.folder && ((type === 'pypi' && /\.(whl|tar\.gz)$/i.test(it.name)) || (type === 'helm' && it.name.endsWith('.tgz')))) {
    items.push({ label: type === 'pypi' ? 'PyPI' : 'Helm', icon: type === 'pypi' ? 'i-lucide-package' : 'i-lucide-ship-wheel', slot: 'package', value: 'package' })
  }
  if (it && !it.folder && isArchive(it.name)) {
    items.push({ label: 'Archive', icon: 'i-lucide-file-archive', slot: 'archive', value: 'archive' })
  }
  if (it && !it.folder && isViewableInBrowser(it.name, it.size)) {
    items.push({ label: 'View', icon: 'i-lucide-file-text', slot: 'view', value: 'view' })
  }
  if (gav.value) {
    items.push({ label: 'Dependency', icon: 'i-lucide-code', slot: 'dependency', value: 'dependency' })
  }
  const b = artifact.value?.builds
  if (b && (b.producedBy.length || b.usedBy.length)) {
    items.push({ label: 'Builds', icon: 'i-lucide-hammer', slot: 'builds', value: 'builds', badge: b.producedBy.length + b.usedBy.length })
  }
  if (views.value) {
    items.push({ label: 'Permissions', icon: 'i-lucide-key-round', slot: 'permissions', value: 'permissions' })
  }
  if (hasSbom.value) {
    items.push({ label: 'SBOM', icon: 'i-lucide-package-search', slot: 'sbom', value: 'sbom', badge: artifact.value?.scan.components || undefined })
  }
  return items
})

const tab = computed({
  get: () => {
    const t = typeof route.query.tab === 'string' ? route.query.tab : 'general'
    return tabs.value.some(i => i.value === t) ? t : 'general'
  },
  set: (v: string | number) => {
    router.replace({ query: { ...route.query, tab: v === 'general' ? undefined : String(v) } })
  }
})

const { data: sbom, status: sbomStatus, error: sbomError, execute: loadSbom, clear: clearSbom } = useAsyncData(
  () => `sbom:${holder.value}/${props.path}`,
  async () => sbomView(await scans.getItemSbom({ repository: holder.value, path: props.path })),
  { immediate: false }
)
const sbomFor = ref('')
watch([tab, hasSbom, () => `${holder.value}/${props.path}`], ([t, has, item]) => {
  if (item !== sbomFor.value || !has) {
    clearSbom()
    sbomFor.value = ''
  }
  if (has && (t === 'sbom' || t === 'security') && !sbomFor.value) {
    sbomFor.value = item
    loadSbom()
  }
}, { immediate: true })
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col">
    <LoadError
      v-if="error"
      :error="error"
      what="the item"
      @retry="refresh()"
    />

    <USkeleton v-else-if="status === 'pending' && !artifact" class="h-48" />

    <div v-else-if="artifact" class="relative flex min-h-0 flex-1 flex-col">
      <!-- The actions sit over the tab bar's end, outside its tablist, which
           may hold tabs only. -->
      <div class="absolute end-0 top-0 flex h-10 items-center gap-1">
        <UButton
          v-if="downloadUrl"
          :to="downloadUrl"
          external
          download
          icon="i-lucide-download"
          label="Download"
          color="neutral"
          variant="outline"
          size="sm"
        />
        <UDropdownMenu :items="actions" :content="{ align: 'end' }">
          <UButton
            icon="i-lucide-ellipsis-vertical"
            color="neutral"
            variant="ghost"
            size="sm"
            aria-label="Actions"
          />
        </UDropdownMenu>
      </div>
      <UTabs
        v-model="tab"
        :items="tabs"
        variant="link"
        class="min-h-0 w-full flex-1"
        :ui="{ list: 'pe-36', content: tab === 'sbom' ? 'flex min-h-0 flex-1 flex-col' : '' }"
      >
        <template #general>
          <dl class="grid grid-cols-1 gap-x-6 gap-y-3 py-4 sm:grid-cols-[max-content_1fr]">
            <template v-for="row in general" :key="row.label">
              <dt class="text-sm text-muted">
                {{ row.label }}
              </dt>
              <dd class="flex items-start gap-1 font-mono text-sm break-all text-default">
                <span>{{ row.value }}</span>
                <UButton
                  v-if="row.copy && row.value"
                  icon="i-lucide-copy"
                  color="neutral"
                  variant="ghost"
                  size="xs"
                  :aria-label="`Copy ${row.label}`"
                  class="-my-1"
                  @click="copy(row.value, `${row.label} copied`)"
                />
              </dd>
            </template>
          </dl>
        </template>

        <template #properties>
          <ArtifactPropertiesTab
            :repo="holder"
            :path="path"
            :folder="artifact.item.folder"
            :properties="artifact.properties"
            :can-edit="artifact.canAnnotate"
            @changed="refresh()"
          />
        </template>

        <template #security>
          <ArtifactSecurityTab :scan="artifact.scan" :components="sbom?.components" />
        </template>

        <template #docker>
          <ArtifactContainerImageTab :repo="holder" :path="path" />
        </template>

        <template #npm>
          <ArtifactNpmPackageTab :repo="holder" :path="path" />
        </template>

        <template #package>
          <ArtifactPackageInfoTab :repo="holder" :path="path" />
        </template>

        <template #archive>
          <ArtifactArchiveTab :repo="holder" :path="path" />
        </template>

        <template #view>
          <ArtifactFileViewer v-if="downloadUrl" :url="downloadUrl" :name="artifact.item.name" />
        </template>

        <template #dependency>
          <ArtifactMavenDependencySnippet v-if="gav" :gav="gav" />
        </template>

        <template #builds>
          <ArtifactBuildsTab :produced-by="artifact.builds.producedBy" :used-by="artifact.builds.usedBy" />
        </template>

        <template #permissions>
          <ArtifactPermissionsTab :repo="holder" :path="path" />
        </template>

        <template #sbom>
          <LoadError
            :error="sbomError"
            what="the SBOM"
            class="mb-4"
            @retry="loadSbom()"
          />
          <ArtifactSbomTab
            :components="sbom?.components ?? []"
            :loading="sbomStatus === 'pending'"
            :export-url="filePath('sbom', holder, path)"
          />
        </template>
      </UTabs>
    </div>
  </div>
</template>
