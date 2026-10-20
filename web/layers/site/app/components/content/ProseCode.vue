<script setup lang="ts">
import type { VNode } from 'vue'
import UProseCode from '@nuxt/ui/components/prose/Code.vue'

const slots = defineSlots<{ default?: () => VNode[] }>()
const placeholders = useCodePlaceholders()

const Content = () => placeholders.vnodes(slots.default?.() ?? [])
</script>

<template>
  <UProseCode>
    <Content />
  </UProseCode>
</template>
