<script setup lang="ts">
import type { VNode } from 'vue'
import UProseCode from '@nuxt/ui/components/prose/Code.vue'

const slots = defineSlots<{ default?: () => VNode[] }>()
const server = useServerAddress()

const Content = () => server.vnodes(slots.default?.() ?? [])
</script>

<template>
  <UProseCode>
    <Content />
  </UProseCode>
</template>
