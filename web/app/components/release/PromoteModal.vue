<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import { RepositoryClass } from '~/gen/relic/v1/repository_pb'

const props = defineProps<{ project: string, name: string, version: string }>()
const emit = defineEmits<{ close: [promoted: boolean] }>()
const { releases, repositories } = useRelic()
const toast = useToast()

const { data: environments, error: environmentsError, refresh: refreshEnvironments } = await useAsyncData(`release:environments`, async () => {
  const { repositories: repos } = await repositories.listRepositories({ class: RepositoryClass.LOCAL })
  return [...new Set(repos.flatMap(r => r.environments))].sort()
})

const schema = z.object({
  environment: z.string().min(1, 'Choose an environment'),
  include: z.array(z.string())
})
type Schema = z.output<typeof schema>

const state = reactive<Schema>({ environment: '', include: [] })
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    await releases.promoteRelease({
      project: props.project,
      name: props.name,
      version: props.version,
      environment: event.data.environment,
      include: event.data.include
    })
    toast.add({ title: `${props.name}/${props.version} promoted to ${event.data.environment}`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Promotion refused', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <UModal
    :title="`Promote ${name}/${version}`"
    description="Relic copies the artifacts to every repository assigned to the environment, if its promotion gates allow."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <LoadError
        :error="environmentsError"
        what="the environments"
        class="mb-4"
        @retry="refreshEnvironments()"
      />
      <UForm
        id="release-promote-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="environment" label="Environment" required>
          <USelectMenu
            v-model="state.environment"
            :items="environments ?? []"
            create-item
            placeholder="Choose an environment"
            class="w-full"
            @create="(e: string) => { state.environment = e }"
          />
        </UFormField>
        <UFormField name="include" label="Only these repositories" hint="Optional">
          <UInputTags v-model="state.include" add-on-blur class="w-full" />
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
        form="release-promote-form"
        label="Promote"
        :loading="saving"
      />
    </template>
  </UModal>
</template>
