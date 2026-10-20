<script setup lang="ts">
import type { VNode } from 'vue'
import UProsePre from '@nuxt/ui/components/prose/Pre.vue'

const props = defineProps<{ code?: string }>()
const slots = defineSlots<{ default?: () => VNode[] }>()
const placeholders = useCodePlaceholders()

const Highlighted = () => placeholders.vnodes(slots.default?.() ?? [])
</script>

<template>
  <UProsePre :code="props.code === undefined ? undefined : placeholders.text(props.code)">
    <Highlighted />
  </UProsePre>
</template>
