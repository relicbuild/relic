<script setup lang="ts">
import type { VNode } from 'vue'
import UProsePre from '@nuxt/ui/components/prose/Pre.vue'

const props = defineProps<{ code?: string }>()
const slots = defineSlots<{ default?: () => VNode[] }>()
const server = useServerAddress()

const Highlighted = () => server.vnodes(slots.default?.() ?? [])
</script>

<template>
  <UProsePre :code="props.code === undefined ? undefined : server.text(props.code)">
    <Highlighted />
  </UProsePre>
</template>
