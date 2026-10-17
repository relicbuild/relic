<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { MessageInitShape } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import type { IgnoreRuleSchema } from '~/gen/relic/v1/scan_pb'

const emit = defineEmits<{ close: [saved: boolean] }>()
const { scans } = useRelic()
const toast = useToast()

const schema = z.object({
  notes: z.string().min(1, 'Say why the risk is accepted'),
  cves: z.array(z.string()),
  pkg: z.string().regex(/^(|pkg:[a-z0-9.+-]+\/[^@?#]+)$/, 'Use a package URL without a version, e.g. pkg:npm/lodash'),
  version: z.string().regex(/^[^@?#\s]*$/, 'A version without "@", "?", "#" or spaces').optional(),
  watches: z.array(z.string()),
  expires: z.string().regex(/^(|\d{4}-\d{2}-\d{2})$/, 'Use YYYY-MM-DD')
}).refine(d => d.cves.length > 0 || d.pkg !== '', { message: 'Give CVEs or a package', path: ['cves'] })
type Schema = z.output<typeof schema>

const state = reactive<Partial<Schema>>({ notes: '', cves: [], pkg: '', version: '', watches: [], expires: '' })
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  const rule: MessageInitShape<typeof IgnoreRuleSchema> = {
    notes: d.notes,
    expires: d.expires ? timestampFromDate(new Date(`${d.expires}T00:00:00Z`)) : undefined,
    cves: d.cves,
    packages: d.pkg ? [d.version ? `${d.pkg}@${d.version}` : d.pkg] : [],
    watches: d.watches
  }
  saving.value = true
  try {
    await scans.createIgnoreRule({ rule })
    toast.add({ title: 'Ignore rule created', color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not create the ignore rule', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    title="New ignore rule"
    description="Matching violations are ignored, and do not fail builds or block downloads."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="ignore-rule-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="notes" label="Reason" required>
          <UTextarea v-model="state.notes" autoresize class="w-full" />
        </UFormField>
        <UFormField name="cves" label="CVEs" description="Press Enter after each one.">
          <UInputTags
            v-model="state.cves"
            add-on-blur
            placeholder="CVE-2020-8840"
            class="w-full"
          />
        </UFormField>
        <UFormField name="pkg" label="Package" description="A package URL without its version.">
          <UInput v-model="state.pkg" placeholder="pkg:npm/lodash" class="w-full" />
        </UFormField>
        <UFormField name="version" label="Package version" hint="Optional">
          <UInput v-model="state.version" placeholder="Any version" class="w-full" />
        </UFormField>
        <UFormField name="watches" label="Only for watches" hint="Optional">
          <UInputTags v-model="state.watches" add-on-blur class="w-full" />
        </UFormField>
        <UFormField name="expires" label="Expires on" hint="Optional">
          <UInput v-model="state.expires" type="date" class="w-full" />
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
        form="ignore-rule-form"
        label="Create"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
