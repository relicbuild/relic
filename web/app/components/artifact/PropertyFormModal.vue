<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'

const props = defineProps<{
  repo: string
  path: string
  folder: boolean
  property?: { key: string, values: string[] }
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { artifacts } = useRelic()
const toast = useToast()

const schema = z.object({
  key: z.string().regex(/^[^\s=;,|]{1,255}$/, 'No spaces or = ; , |'),
  values: z.array(z.string().max(4096)).min(1, 'Add at least one value (press Enter after each)').max(100, 'At most 100 values'),
  recursive: z.boolean()
})
type Schema = z.output<typeof schema>

const state = reactive<Schema>({
  key: props.property?.key ?? '',
  values: [...(props.property?.values ?? [])],
  recursive: false
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    await artifacts.setProperties({ repository: props.repo, path: props.path, properties: { [event.data.key]: { values: event.data.values } }, recursive: event.data.recursive })
    toast.add({ title: `Property ${event.data.key} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the property', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <UModal
    :title="property ? `Edit ${property.key}` : 'Add property'"
    :close="{ onClick: () => emit('close', false) }"
    :ui="{ footer: 'justify-end' }"
  >
    <template #body>
      <UForm
        id="property-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="key" label="Key" required>
          <UInput v-model="state.key" :disabled="!!property" class="w-full" />
        </UFormField>
        <UFormField
          name="values"
          label="Values"
          description="Press Enter after each value."
          required
        >
          <UInputTags v-model="state.values" add-on-blur class="w-full" />
        </UFormField>
        <UFormField v-if="folder" name="recursive">
          <USwitch v-model="state.recursive" label="Apply to everything in this folder" />
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
        form="property-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </UModal>
</template>
