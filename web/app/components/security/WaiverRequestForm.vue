<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'

const emit = defineEmits<{ close: [saved: boolean] }>()
const { waivers } = useRelic()
const toast = useToast()

const schema = z.object({
  reason: z.string().min(1, 'Say why the findings do not apply'),
  vulnerabilities: z.array(z.string()),
  pkg: z.string().regex(/^(|pkg:[a-z0-9.+-]+\/[^?#]+)$/, 'Use a package URL, e.g. pkg:npm/lodash@4.17.15'),
  licenses: z.array(z.string())
}).refine(d => d.vulnerabilities.length > 0 || d.pkg !== '' || d.licenses.length > 0, {
  message: 'Give vulnerabilities, a package or licenses',
  path: ['vulnerabilities']
})
type Schema = z.output<typeof schema>

const state = reactive<Partial<Schema>>({ reason: '', vulnerabilities: [], pkg: '', licenses: [] })
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  saving.value = true
  try {
    await waivers.requestWaiver({
      reason: d.reason,
      rule: {
        vulnerabilities: d.vulnerabilities,
        packages: d.pkg ? [d.pkg] : [],
        licenses: d.licenses
      }
    })
    toast.add({ title: 'Waiver requested', color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not request the waiver', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    title="Request a waiver"
    description="Administrators approve it for a number of days, as an ignore rule, or reject it."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="waiver-request-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="reason" label="Reason" required>
          <UTextarea v-model="state.reason" autoresize class="w-full" />
        </UFormField>
        <UFormField name="vulnerabilities" label="Vulnerabilities" description="Press Enter after each one.">
          <UInputTags
            v-model="state.vulnerabilities"
            add-on-blur
            placeholder="CVE-2020-8203"
            class="w-full"
          />
        </UFormField>
        <UFormField name="pkg" label="Package" description="A package URL; without a version it covers every version.">
          <UInput v-model="state.pkg" placeholder="pkg:npm/lodash@4.17.15" class="w-full" />
        </UFormField>
        <UFormField name="licenses" label="Licenses" hint="Optional">
          <UInputTags
            v-model="state.licenses"
            add-on-blur
            placeholder="GPL-3.0"
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
        form="waiver-request-form"
        label="Request"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
