<script setup lang="ts">
import type { NavigationMenuItem } from '@nuxt/ui'

const open = ref(false)
const { me, views, signOut } = useSession()
const toast = useToast()

async function onSignOut() {
  try {
    await signOut()
    await navigateTo('/login')
  } catch (err) {
    toast.add({ title: 'Could not sign out', description: relicErrorMessage(err, 'The server could not be reached.'), color: 'error' })
  }
}

const overlay = useOverlay()
const stopClosing = useRouter().afterEach((to, from) => {
  if (to.path !== from.path) {
    overlay.closeAll()
  }
})
onBeforeUnmount(stopClosing)

const close = () => {
  open.value = false
}

const mainItems = computed<NavigationMenuItem[][]>(() => [
  [
    { label: 'Home', icon: 'i-lucide-house', to: '/dashboard', exact: true, onSelect: close }
  ],
  [
    { type: 'label', label: 'Artifacts' },
    { label: 'Browse', icon: 'i-lucide-folder-tree', to: '/browse', onSelect: close },
    { label: 'Repositories', icon: 'i-lucide-boxes', to: '/repositories', onSelect: close },
    { label: 'Builds', icon: 'i-lucide-hammer', to: '/builds', onSelect: close },
    { label: 'Releases', icon: 'i-lucide-package-check', to: '/releases', onSelect: close },
    { label: 'Projects', icon: 'i-lucide-folder-kanban', to: '/projects', onSelect: close },
    { label: 'Agent registry', icon: 'i-lucide-plug', to: '/agent-registry', onSelect: close },
    { label: 'Set me up', icon: 'i-lucide-terminal', to: '/client-setup', onSelect: close }
  ],
  ...(views.value
    ? [[
      { type: 'label', label: 'Security' },
      { label: 'Vulnerabilities', icon: 'i-lucide-shield-alert', to: '/security', exact: true, onSelect: close },
      { label: 'CVEs', icon: 'i-lucide-bug', to: '/security/cves', onSelect: close },
      { label: 'Operational risk', icon: 'i-lucide-calendar-x', to: '/security/operational-risk', onSelect: close },
      { label: 'Policies', icon: 'i-lucide-scale', to: '/security/policies', onSelect: close },
      { label: 'Watches', icon: 'i-lucide-eye', to: '/security/watches', onSelect: close },
      { label: 'Ignore rules', icon: 'i-lucide-bell-off', to: '/security/ignore-rules', onSelect: close },
      { label: 'Quarantine', icon: 'i-lucide-shield-half', to: '/security/quarantine', onSelect: close },
      { label: 'Waivers', icon: 'i-lucide-file-check', to: '/security/waivers', onSelect: close },
      { label: 'Decisions', icon: 'i-lucide-gavel', to: '/security/decisions', onSelect: close },
      { label: 'Promotion gates', icon: 'i-lucide-shield-check', to: '/security/promotion-gates', onSelect: close }
    ], [
      { type: 'label', label: 'Administration' },
      { label: 'Users', icon: 'i-lucide-user', to: '/admin/users', onSelect: close },
      { label: 'Groups', icon: 'i-lucide-users', to: '/admin/groups', onSelect: close },
      { label: 'Permissions', icon: 'i-lucide-key-round', to: '/admin/permissions', onSelect: close },
      { label: 'Access tokens', icon: 'i-lucide-key-square', to: '/admin/tokens', onSelect: close },
      { label: 'LDAP', icon: 'i-lucide-book-user', to: '/admin/ldap', onSelect: close },
      { label: 'Single sign-on', icon: 'i-lucide-key-round', to: '/admin/sso', onSelect: close },
      { label: 'Workload identities', icon: 'i-lucide-workflow', to: '/admin/workload-identities', onSelect: close },
      { label: 'Webhooks', icon: 'i-lucide-webhook', to: '/admin/webhooks', onSelect: close },
      { label: 'Replication', icon: 'i-lucide-refresh-ccw', to: '/admin/replications', onSelect: close },
      { label: 'Cleanup policies', icon: 'i-lucide-brush-cleaning', to: '/admin/cleanup', onSelect: close },
      { label: 'Trash can', icon: 'i-lucide-trash-2', to: '/admin/trash', onSelect: close },
      { label: 'Audit log', icon: 'i-lucide-scroll-text', to: '/admin/audit', onSelect: close },
      { label: 'Usage', icon: 'i-lucide-chart-column', to: '/admin/usage', onSelect: close },
      { label: 'System', icon: 'i-lucide-server', to: '/admin/system', onSelect: close }
    ]] satisfies NavigationMenuItem[][]
    : [[
      { type: 'label', label: 'Security' },
      { label: 'Waivers', icon: 'i-lucide-file-check', to: '/security/waivers', onSelect: close }
    ]] satisfies NavigationMenuItem[][])
])

const { artifacts, system } = useRelic()
const { data: serverVersion } = useAsyncData('server:version', () => system.getVersion({}), { server: false, lazy: true })
const searchTerm = ref('')
const term = useDebounced(searchTerm)
const { data: searchRepos, refresh: refreshSearchRepos } = useAsyncData('search:repositories', listRepoRows, { server: false, lazy: true, default: () => [] })
const searchOpen = ref(false)
watch(searchOpen, (opened) => {
  if (opened) {
    refreshSearchRepos()
  }
})
const { data: found, status: foundStatus, error: foundError } = useAsyncData(
  () => `search:items:${term.value}`,
  () => term.value.trim().length >= 2
    ? artifacts.searchItems({ name: term.value.trim(), limit: 10 }).then(r => r.items.slice(0, 20))
    : Promise.resolve([]),
  { watch: [term], server: false, lazy: true, default: () => [] }
)
const searchGroups = computed(() => [{
  id: 'links',
  label: 'Go to',
  items: mainItems.value.flat().filter(i => i.to).map(i => ({ label: i.label, icon: i.icon, to: i.to, onSelect: close }))
}, {
  id: 'repositories',
  label: 'Repositories',
  items: (searchRepos.value ?? []).map(r => ({ label: r.name, suffix: `${r.format}, ${r.class}`, icon: formatIcon(r.format), to: `/repositories/${r.name}`, onSelect: close }))
}, {
  id: 'artifacts',
  label: 'Artifacts',
  ignoreFilter: true,
  items: foundError.value
    ? [{ label: relicErrorMessage(foundError.value, 'The search failed.'), icon: 'i-lucide-circle-alert', disabled: true }]
    : (found.value ?? []).map(it => ({
        label: it.name,
        suffix: `${it.repository}/${it.path}`,
        icon: fileIcon(it.name, it.folder) ?? 'i-lucide-folder',
        to: { path: `/repositories/${it.repository}`, query: { path: it.path } },
        onSelect: close
      }))
}])
</script>

<template>
  <UDashboardGroup unit="rem" storage-key="relic-dashboard">
    <UDashboardSidebar
      id="default"
      v-model:open="open"
      collapsible
      resizable
      class="bg-elevated/25"
      :ui="{ footer: 'flex-col items-stretch gap-2.5 border-t border-default' }"
    >
      <template #header="{ collapsed }">
        <NuxtLink to="/" class="flex items-center gap-2">
          <UIcon name="i-lucide-package" class="size-6 shrink-0 text-primary" />
          <span v-if="!collapsed" class="text-lg font-bold text-highlighted">Relic</span>
        </NuxtLink>
        <UTooltip
          v-if="!collapsed && serverVersion?.version"
          :text="serverVersion.revision ? `${serverVersion.version}, revision ${serverVersion.revision.slice(0, 12)}` : serverVersion.version"
        >
          <span class="ms-auto min-w-0 truncate font-mono text-xs text-muted">{{ serverVersion.version }}</span>
        </UTooltip>
      </template>

      <template #default="{ collapsed }">
        <UDashboardSearchButton :collapsed="collapsed" class="bg-transparent ring-default" />
        <UNavigationMenu
          :collapsed="collapsed"
          :items="mainItems"
          orientation="vertical"
          tooltip
          popover
        />
      </template>

      <template #footer="{ collapsed }">
        <UUser
          v-if="!collapsed && me"
          :name="me.username"
          :description="me.admin ? 'Administrator' : me.viewer ? 'Viewer' : 'User'"
          :avatar="{ text: me.username.slice(0, 2).toUpperCase(), alt: me.username }"
        />
        <div class="flex items-center gap-2" :class="collapsed ? 'flex-col' : ''">
          <UButton
            color="neutral"
            variant="outline"
            icon="i-lucide-log-out"
            :label="collapsed ? undefined : 'Sign out'"
            :aria-label="`Sign out ${me?.username ?? ''}`"
            :square="collapsed"
            block
            @click="onSignOut"
          />
          <UColorModeButton color="neutral" variant="outline" />
        </div>
      </template>
    </UDashboardSidebar>

    <UDashboardSearch
      v-model:open="searchOpen"
      v-model:search-term="searchTerm"
      :groups="searchGroups"
      :loading="foundStatus === 'pending'"
      title="Search"
      description="Pages, repositories and artifacts"
      placeholder="Search pages, repositories and files…"
    />

    <slot />
  </UDashboardGroup>
</template>
