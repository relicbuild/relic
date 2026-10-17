<script setup lang="ts">
import type { ContentNavigationItem } from '@nuxt/content'

const { data: navigation } = await useAsyncData(
  'docs-navigation',
  () => queryCollectionNavigation('docs'),
  { transform: (data: ContentNavigationItem[]) => data.find(item => item.path === '/docs')?.children || data }
)

provide('navigation', navigation)
</script>

<template>
  <div class="min-h-screen flex flex-col">
    <PublicSiteHeader
      subtitle="Docs"
      search
    />
    <UMain class="flex-1">
      <UContainer>
        <UPage>
          <template #left>
            <UPageAside>
              <DocsAsideLeftTop />
              <DocsAsideLeftBody />
            </UPageAside>
          </template>
          <slot />
        </UPage>
      </UContainer>
    </UMain>
    <ClientOnly>
      <AppSearch :navigation="navigation ?? undefined" />
    </ClientOnly>
  </div>
</template>
