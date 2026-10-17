<script setup lang="ts">
import type { TabsItem } from '@nuxt/ui'

const props = defineProps<{ gav: MavenCoordinates }>()
const copy = useCopy()

const snippets = computed(() => {
  const { group, artifact, version, classifier } = props.gav
  const c = classifier ? `:${classifier}` : ''
  return {
    maven: [
      '<dependency>',
      `  <groupId>${group}</groupId>`,
      `  <artifactId>${artifact}</artifactId>`,
      `  <version>${version}</version>`,
      ...(classifier ? [`  <classifier>${classifier}</classifier>`] : []),
      '</dependency>'
    ].join('\n'),
    gradle: `implementation("${group}:${artifact}:${version}${c}")`,
    ivy: `<dependency org="${group}" name="${artifact}" rev="${version}"${classifier ? ` m:classifier="${classifier}"` : ''} />`,
    sbt: `libraryDependencies += "${group}" % "${artifact}" % "${version}"${classifier ? ` classifier "${classifier}"` : ''}`
  }
})

const tools: TabsItem[] = [
  { label: 'Maven', value: 'maven' },
  { label: 'Gradle', value: 'gradle' },
  { label: 'Ivy', value: 'ivy' },
  { label: 'SBT', value: 'sbt' }
]
const tool = ref<keyof typeof snippets.value>('maven')
</script>

<template>
  <div class="space-y-3 py-4">
    <div class="flex items-center justify-between gap-2">
      <UTabs
        v-model="tool"
        :items="tools"
        :content="false"
        size="sm"
        class="w-fit"
      />
      <UButton
        icon="i-lucide-copy"
        color="neutral"
        variant="ghost"
        size="xs"
        aria-label="Copy snippet"
        @click="copy(snippets[tool], 'Snippet copied')"
      />
    </div>
    <pre class="overflow-x-auto rounded-md border border-default bg-elevated/50 p-3 font-mono text-xs text-default">{{ snippets[tool] }}</pre>
  </div>
</template>
