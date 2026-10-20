<script setup lang="ts">
import type { ButtonProps } from '@nuxt/ui'

useSeoMeta({
  description: 'Relic: a self-hosted artifact repository in one binary, backed by S3 or a local directory and PostgreSQL or SQLite.'
})

const copy = useCopy()
const server = useServerAddress()
const installOrigin = useAppConfig().relic.dashboard ? useRequestURL().origin : 'https://relic.build'

const connectCommands = [
  { label: 'install', icon: 'i-lucide-download', command: `curl -fsSL ${installOrigin}/install.sh | sh` },
  { label: 'relic', icon: 'i-lucide-package', command: `relic auth login ${server.origin}` },
  { label: 'jf', icon: 'i-lucide-terminal', command: `jf config add relic --url ${server.origin}` }
]

const links: ButtonProps[] = [
  { label: 'Read the docs', to: '/docs', size: 'lg', color: 'neutral', variant: 'subtle', icon: 'i-lucide-book-open' },
  { label: 'API reference', to: '/api-reference', target: '_blank', size: 'lg', color: 'neutral', variant: 'subtle', icon: 'i-lucide-braces', trailingIcon: 'i-lucide-external-link' }
]
</script>

<template>
  <UMain class="flex flex-col justify-center lg:min-h-0 lg:flex-1">
    <UPageHero
      description="One self-hosted binary for your packages and images. Files go to S3 or a local directory, metadata to PostgreSQL or SQLite. MIT licensed."
      orientation="horizontal"
      :ui="{ title: 'text-4xl sm:text-5xl font-medium tracking-tight text-highlighted text-pretty', container: 'lg:py-6', headline: 'lg:mb-3', description: 'lg:mt-4', footer: 'lg:mt-7' }"
    >
      <template #headline>
        <div class="flex flex-col gap-6 lg:gap-4">
          <span class="inline-flex items-center gap-4 text-6xl sm:text-7xl font-semibold tracking-tight text-highlighted">
            <UIcon name="i-lucide-package" class="size-14 sm:size-16 shrink-0 text-primary" />
            Relic
          </span>
          <span class="font-mono text-xs uppercase tracking-[0.22em] text-muted">Self-hosted artifact repository</span>
        </div>
      </template>

      <template #title>
        Artifact repository<br>
        on <LandingRotatingWord :words="['S3', 'filesystem']" class="text-primary" />
        and <LandingRotatingWord :words="['PostgreSQL', 'SQLite']" :delay="300" class="text-primary" />
      </template>

      <template #footer>
        <div class="flex flex-wrap gap-x-6 gap-y-3">
          <SiteAccountButton size="lg" />
          <UButton v-for="link in links" :key="link.label" v-bind="link" />
        </div>
        <UTabs
          :items="connectCommands"
          variant="link"
          size="sm"
          class="mt-8 lg:mt-6 max-w-xl gap-0 overflow-hidden rounded-md border border-default bg-elevated/50"
          :ui="{ list: 'border-b border-default px-2', trigger: 'font-mono' }"
        >
          <template #content="{ item }">
            <div class="flex items-center gap-3 py-2 ps-4 pe-2 font-mono text-sm">
              <span class="text-primary" aria-hidden="true">$</span>
              <code class="min-w-0 flex-1 truncate text-toned">{{ item.command }}</code>
              <UButton
                icon="i-lucide-copy"
                color="neutral"
                variant="ghost"
                size="sm"
                :aria-label="`Copy the ${item.label} command`"
                @click="copy(item.command, 'Command copied')"
              />
            </div>
          </template>
        </UTabs>
      </template>

      <div class="hidden lg:flex lg:flex-col lg:items-center lg:gap-6">
        <LandingPackageOrbit />
        <p class="font-mono text-xs uppercase tracking-[0.2em] text-muted">
          One registry for every package client
        </p>
      </div>
    </UPageHero>
  </UMain>
</template>
