<script setup lang="ts">
const props = defineProps<{ repo: string, path: string }>()
const copy = useCopy()

const { data: info, status, error, refresh } = useAsyncData(
  () => `package:${props.repo}/${props.path}`,
  () => packageInfo(props.repo, props.path),
  { watch: [() => props.repo, () => props.path] }
)

const facts = computed(() => {
  const p = info.value?.pypi
  const h = info.value?.helm
  const rows: [string, string | undefined][] = p
    ? [['Package', `${p.name} ${p.version}`], ['Summary', p.summary], ['License', p.license], ['Requires Python', p.requiresPython], ['Author', p.author], ['Home page', p.homePage]]
    : h
      ? [['Chart', `${h.name} ${h.version}`], ['App version', h.appVersion], ['Description', h.description], ['Type', h.type], ['API version', h.apiVersion], ['Kubernetes', h.kubeVersion], ['Home', h.home]]
      : []
  return rows.filter((r): r is [string, string] => !!r[1])
})

const install = computed(() => {
  const p = info.value?.pypi
  const h = info.value?.helm
  if (p) {
    return `pip install ${shellWord(`${p.name}==${p.version}`)}`
  }
  return h ? `helm install ${shellWord(h.name)} ${shellWord(`${props.repo}/${h.name}`)} --version ${shellWord(h.version)}` : ''
})
</script>

<template>
  <div class="flex flex-col gap-6 py-4">
    <LoadError
      v-if="error"
      :error="error"
      what="the package metadata"
      @retry="refresh()"
    />
    <USkeleton v-else-if="status === 'pending' && !info" class="h-40" />

    <template v-else-if="info">
      <UAlert
        v-if="info.helm?.deprecated"
        color="warning"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        title="This chart is deprecated"
      />
      <dl class="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-[max-content_1fr]">
        <template v-for="[label, value] in facts" :key="label">
          <dt class="text-sm text-muted">
            {{ label }}
          </dt>
          <dd class="text-sm break-all text-default">
            {{ value }}
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

      <template v-if="info.pypi">
        <div v-if="info.pypi.requiresDist.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Requirements
          </h3>
          <ul class="space-y-1 font-mono text-xs text-default">
            <li v-for="r in info.pypi.requiresDist" :key="r">
              {{ r }}
            </li>
          </ul>
        </div>
        <div v-if="info.pypi.projectUrls.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Project links
          </h3>
          <ul class="space-y-1 text-sm">
            <li v-for="u in info.pypi.projectUrls" :key="u" class="break-all">
              {{ u }}
            </li>
          </ul>
        </div>
        <div v-if="info.pypi.classifiers.length" class="flex flex-wrap gap-1">
          <UBadge
            v-for="c in info.pypi.classifiers"
            :key="c"
            color="neutral"
            variant="subtle"
            :label="c"
          />
        </div>
      </template>

      <template v-if="info.helm">
        <div v-if="info.helm.dependencies.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Dependencies
          </h3>
          <UTable
            :data="info.helm.dependencies"
            :columns="[{ accessorKey: 'name', header: 'Chart' }, { accessorKey: 'version', header: 'Version' }, { accessorKey: 'repository', header: 'Repository' }, { accessorKey: 'condition', header: 'Condition' }]"
          />
        </div>
        <div v-if="info.helm.maintainers.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Maintainers
          </h3>
          <ul class="space-y-1 text-sm text-default">
            <li v-for="m in info.helm.maintainers" :key="m.name">
              {{ m.name }}<span v-if="m.email" class="text-muted"> · {{ m.email }}</span>
            </li>
          </ul>
        </div>
        <div v-if="info.helm.keywords.length" class="flex flex-wrap gap-1">
          <UBadge
            v-for="k in info.helm.keywords"
            :key="k"
            color="neutral"
            variant="subtle"
            :label="k"
          />
        </div>
      </template>
    </template>
  </div>
</template>
