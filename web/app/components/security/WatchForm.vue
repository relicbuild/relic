<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import { PolicyKind, WatchScopeKind, type Watch } from '~/gen/relic/v1/scan_pb'

const props = defineProps<{ watch?: Watch }>()
const emit = defineEmits<{ close: [saved: boolean] }>()

const { builds: buildClient, scans } = useRelic()
const toast = useToast()

// Not awaited: overlays are mounted without a Suspense boundary.
const { data: options, status, error, refresh } = useAsyncData('watch-form:options', async () => {
  const [repos, builds, policies] = await Promise.all([
    listRepoRows(),
    buildClient.listBuilds({}).then(r => r.builds),
    scans.listPolicies({}).then(r => r.policies)
  ])
  return { repos, builds, policies }
})

const resourceItems = computed(() => [
  { label: 'All repositories', value: 'repository:' },
  { label: 'All builds', value: 'build:' },
  ...(options.value?.repos ?? []).filter(r => r.class !== 'virtual').map(r => ({ label: `Repository ${r.name}`, value: `repository:${r.name}` })),
  ...(options.value?.builds ?? []).map(b => ({ label: `Build ${b.name}`, value: `build:${b.name}` }))
])
const policyItems = computed(() => (options.value?.policies ?? []).map(p => ({ label: `${p.name} (${enumName(PolicyKind, p.kind)})`, value: p.name })))

const schema = z.object({
  name: z.string().regex(/^[\w.-]+$/, 'Letters, digits, dot, dash and underscore only'),
  description: z.string().optional(),
  active: z.boolean(),
  resources: z.array(z.string()).min(1, 'Choose what the watch covers'),
  policies: z.array(z.string()).min(1, 'Assign at least one policy')
})
type Schema = z.output<typeof schema>

const state = reactive<Partial<Schema>>({
  name: props.watch?.name ?? '',
  description: props.watch?.description ?? '',
  active: props.watch?.active ?? true,
  resources: props.watch?.scopes.flatMap(s => [...(s.all ? [''] : []), ...s.names].map(n => `${enumName(WatchScopeKind, s.kind)}:${n}`)) ?? [],
  policies: [...(props.watch?.policies ?? [])]
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  const watch = {
    name: d.name,
    description: d.description,
    active: d.active,
    scopes: (['repository', 'build'] as const).flatMap((kind) => {
      const picked = d.resources.filter(v => v.startsWith(`${kind}:`)).map(v => v.slice(kind.length + 1))
      return picked.length ? [{ kind: enumValue(WatchScopeKind, kind), all: picked.includes(''), names: picked.filter(n => n !== '') }] : []
    }),
    policies: d.policies
  }
  saving.value = true
  try {
    if (!props.watch && (await scans.listWatches({})).watches.some(w => w.name === d.name)) {
      throw new Error(`watch ${d.name} already exists`)
    }
    await scans.putWatch({ watch })
    toast.add({ title: `Watch ${d.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the watch', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="watch ? `Edit ${watch.name}` : 'New watch'"
    description="A watch applies policies to repositories and builds."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <LoadError
        :error="error"
        what="repositories, builds and policies"
        class="mb-4"
        @retry="refresh()"
      />
      <UForm
        id="watch-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="name" label="Name" required>
          <UInput v-model="state.name" :disabled="!!watch" class="w-full" />
        </UFormField>
        <UFormField name="description" label="Description">
          <UInput v-model="state.description" class="w-full" />
        </UFormField>
        <UFormField name="active">
          <USwitch v-model="state.active" label="Active" />
        </UFormField>
        <UFormField name="resources" label="Covers" required>
          <USelectMenu
            v-model="state.resources"
            :items="resourceItems"
            :loading="status === 'pending'"
            value-key="value"
            multiple
            placeholder="Repositories and builds"
            class="w-full"
          />
        </UFormField>
        <UFormField name="policies" label="Policies" required>
          <USelectMenu
            v-model="state.policies"
            :items="policyItems"
            value-key="value"
            multiple
            placeholder="Policies to apply"
            class="w-full"
          />
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
        form="watch-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
