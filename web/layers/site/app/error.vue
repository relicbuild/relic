<script setup lang="ts">
import type { NuxtError } from '#app'

const props = defineProps<{
  error: NuxtError
}>()

useSeoMeta({
  title: props.error.statusCode === 404 ? 'Page not found' : 'Something went wrong'
})

useHead({
  htmlAttrs: {
    lang: 'en'
  }
})
</script>

<template>
  <UApp>
    <UError :error="error" />
  </UApp>
</template>
