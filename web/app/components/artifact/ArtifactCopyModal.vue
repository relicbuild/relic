<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'

const props = defineProps<{
  repo: string
  path: string
  name: string
  move: boolean
}>()
const emit = defineEmits<{ close: [done: boolean] }>()
const { artifacts, repositories } = useRelic()
const toast = useToast()

async function uploadableTargets(): Promise<string[]> {
  const rows = await listRepoRows()
  const format = rows.find(r => r.name === props.repo)?.format
  const candidates = rows.filter(r => r.class === 'local' && r.format === format).map(r => r.name)
  const rights = await Promise.all(candidates.map(name => repositories.getRepository({ name })))
  return candidates.filter((_, i) => rights[i]?.canUpload)
}

// Not awaited: overlays are mounted without a Suspense boundary.
const { data: targets, status, error, refresh } = useAsyncData(`copy-targets:${props.repo}`, uploadableTargets, { default: () => [] })

const schema = z.object({
  toRepository: z.string().min(1, 'Choose a repository'),
  toPath: z.string().min(1, 'Give the path inside the repository')
})
type Schema = z.output<typeof schema>
const state = reactive<Partial<Schema>>({ toRepository: '', toPath: props.path })
const saving = ref(false)
const verb = props.move ? 'move' : 'copy'

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  const req = { repository: props.repo, path: props.path, toRepository: event.data.toRepository, toPath: event.data.toPath.replace(/^\/+|\/+$/g, '') }
  try {
    await (props.move ? artifacts.moveItem(req) : artifacts.copyItem(req))
    toast.add({ title: `${props.name} ${props.move ? 'moved' : 'copied'} to ${req.toRepository}/${req.toPath}`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: `Could not ${verb} ${props.name}`, description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <UModal
    :title="`${move ? 'Move' : 'Copy'} ${name}`"
    :description="`From ${repo}/${path}`"
    :close="{ onClick: () => emit('close', false) }"
    :ui="{ footer: 'justify-end' }"
  >
    <template #body>
      <LoadError
        :error="error"
        what="repositories"
        class="mb-4"
        @retry="refresh()"
      />
      <UForm
        id="artifact-copy-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField
          name="toRepository"
          label="To repository"
          :description="status === 'success' && !targets.length ? 'You may not upload to any local repository of this format.' : 'Local repositories of the same format that you may upload to.'"
          required
        >
          <USelectMenu
            v-model="state.toRepository"
            :items="targets"
            placeholder="Repository"
            class="w-full"
          />
        </UFormField>
        <UFormField name="toPath" label="Path" required>
          <UInput v-model="state.toPath" class="w-full font-mono" />
        </UFormField>
      </UForm>
    </template>
    <template #footer>
      <UButton
        label="Cancel"
        color="neutral"
        variant="outline"
        @click="emit('close', false)"
      />
      <UButton
        type="submit"
        form="artifact-copy-form"
        :label="move ? 'Move' : 'Copy'"
        :loading="saving"
      />
    </template>
  </UModal>
</template>
