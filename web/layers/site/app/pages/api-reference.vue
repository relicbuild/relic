<script setup lang="ts">
definePageMeta({ layout: false })

const colorMode = useColorMode()

// The site publishes the description next to its pages; a server serves its own.
const specURL = useAppConfig().relic.dashboard ? '/api/v1/openapi.json' : '/openapi.json'
const { data: spec, error, refresh } = await useAsyncData('openapi', () => $fetch<string>(specURL, { responseType: 'text' }), { server: false })

const configuration = computed(() => apiReferenceConfiguration(spec.value ?? '', colorMode.value === 'dark'))
</script>

<template>
  <div class="min-h-screen flex flex-col bg-default">
    <PublicSiteHeader subtitle="API" />
    <UAlert
      v-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-circle-alert"
      title="Could not load the API description"
      :description="error.message"
      :actions="[{ label: 'Retry', icon: 'i-lucide-refresh-cw', color: 'error', variant: 'outline', onClick: () => refresh() }]"
      class="m-4"
    />
    <ClientOnly v-else-if="spec">
      <div class="flex-1" style="--scalar-custom-header-height: var(--ui-header-height)">
        <ScalarApiReference :configuration="configuration" />
      </div>
    </ClientOnly>
  </div>
</template>
