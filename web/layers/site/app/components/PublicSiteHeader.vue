<script setup lang="ts">
defineProps<{ subtitle?: string, search?: boolean }>()

const route = useRoute()
</script>

<template>
  <UHeader :toggle="false" to="/">
    <template #title>
      <UIcon name="i-lucide-package" class="size-6 shrink-0 text-primary" />
      <span class="text-lg font-bold text-highlighted">Relic</span>
      <span v-if="subtitle" class="text-sm font-normal text-muted">{{ subtitle }}</span>
    </template>

    <template #right>
      <UContentSearchButton v-if="search" :collapsed="false" class="w-56" />
      <UButton
        to="/docs"
        color="neutral"
        variant="ghost"
        icon="i-lucide-book-open"
        label="Docs"
      />
      <UButton
        v-if="route.path !== '/api-reference'"
        to="/api-reference"
        target="_blank"
        color="neutral"
        variant="ghost"
        icon="i-lucide-braces"
        trailing-icon="i-lucide-external-link"
        label="API"
      />
      <SiteAccountButton
        color="neutral"
        variant="ghost"
      />
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
