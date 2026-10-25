<script setup lang="ts">
defineProps<{ subtitle?: string, search?: boolean }>()

const route = useRoute()
const onLandingPage = computed(() => route.path === '/')
</script>

<template>
  <UHeader :toggle="false" to="/">
    <template #title>
      <UIcon name="i-lucide-package" class="size-6 shrink-0 text-primary" />
      <span class="text-lg font-bold text-highlighted">Relic</span>
      <span v-if="subtitle" class="text-sm font-normal text-muted">{{ subtitle }}</span>
    </template>

    <template #right>
      <template v-if="search">
        <UContentSearchButton :collapsed="false" class="hidden w-56 sm:flex" />
        <UContentSearchButton class="sm:hidden" />
      </template>
      <template v-if="!onLandingPage">
        <UButton
          to="/docs"
          class="hidden sm:inline-flex"
          color="neutral"
          variant="ghost"
          icon="i-lucide-book-open"
          label="Docs"
        />
        <UButton
          v-if="route.path !== '/api-reference'"
          to="/api-reference"
          class="hidden sm:inline-flex"
          target="_blank"
          color="neutral"
          variant="ghost"
          icon="i-lucide-braces"
          trailing-icon="i-lucide-external-link"
          label="API"
        />
        <SiteAccountButton
          compact
          color="neutral"
          variant="ghost"
        />
      </template>
      <UButton
        to="https://github.com/relicbuild/relic"
        target="_blank"
        color="neutral"
        variant="ghost"
        icon="i-simple-icons-github"
        aria-label="Relic on GitHub"
      />
      <UColorModeButton />
    </template>
  </UHeader>
</template>
