<script setup lang="ts">
import type { NavigationMenuItem } from '@nuxt/ui'

const year = new Date().getFullYear()

const links: NavigationMenuItem[] = [
  { label: 'Docs', to: '/docs' },
  { label: 'API reference', to: '/api-reference', target: '_blank' }
]
</script>

<template>
  <UFooter :ui="{ root: 'border-t border-default' }">
    <template #left>
      <UIcon name="i-lucide-package" class="size-5 shrink-0 text-primary" />
      <p class="text-sm text-muted">
        © {{ year }} The Relic Authors · MIT licensed
      </p>
    </template>

    <UNavigationMenu :items="links" variant="link" />

    <template #right>
      <UButton
        to="https://github.com/relicbuild/relic"
        target="_blank"
        color="neutral"
        variant="ghost"
        icon="i-simple-icons-github"
        aria-label="Relic on GitHub"
      />
    </template>
  </UFooter>
</template>
