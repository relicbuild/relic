<script setup lang="ts">
const props = defineProps<{
  url: string
  name?: string
}>()

const { data: text, status, error, refresh } = useFetch<string>(() => props.url, {
  key: () => `view:${props.url}`,
  responseType: 'text',
  baseURL: '/',
  lazy: true
})
const lines = computed(() => {
  let t = text.value ?? ''
  if (props.name?.toLowerCase().endsWith('.json')) {
    try {
      t = JSON.stringify(JSON.parse(t), null, 2)
    } catch {
      // not valid JSON: show it as it is
    }
  }
  return t.split('\n')
})
</script>

<template>
  <div class="py-4">
    <LoadError
      v-if="error"
      :error="error"
      what="the file"
      @retry="refresh()"
    />
    <USkeleton v-else-if="status === 'pending'" class="h-64" />
    <div v-else class="max-h-[70vh] overflow-auto rounded-md border border-default bg-elevated/50">
      <table class="font-mono text-xs">
        <tbody>
          <tr v-for="(line, i) in lines" :key="i">
            <td class="sticky left-0 bg-elevated px-3 text-right text-dimmed select-none">
              {{ i + 1 }}
            </td>
            <td class="px-3 whitespace-pre text-default">
              {{ line }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
