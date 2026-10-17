<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { PromotionGate } from '~/gen/relic/v1/promotion_gate_pb'

const props = defineProps<{ gate?: PromotionGate, taken: string[] }>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { promotionGates } = useRelic()
const toast = useToast()

const schema = z.object({
  name: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/, 'Letters, digits, ".", "_", "+" or "-"')
    .refine(n => !!props.gate || !props.taken.includes(n), 'A gate with this name exists'),
  environment: z.string().min(1, 'The environment the gate guards'),
  condition: z.string().min(1, 'A CEL condition that must be true'),
  description: z.string()
})
type Schema = z.output<typeof schema>

const state = reactive<Schema>({
  name: props.gate?.name ?? '',
  environment: props.gate?.environment ?? '',
  condition: props.gate?.condition ?? 'signed && scan.critical == 0',
  description: props.gate?.description ?? ''
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    await promotionGates.putPromotionGate({ promotionGate: { ...event.data } })
    toast.add({ title: `Gate ${event.data.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the gate', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="gate ? `Edit gate ${gate.name}` : 'New promotion gate'"
    description="Promotions to the environment are refused unless the condition holds."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="promotion-gate-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="name" label="Name" required>
          <UInput v-model="state.name" :disabled="!!gate" class="w-full" />
        </UFormField>
        <UFormField name="environment" label="Environment" required>
          <UInput v-model="state.environment" placeholder="PROD" class="w-full" />
        </UFormField>
        <UFormField
          name="condition"
          label="Condition"
          description="CEL over release, environment, evidence, scan (critical, high, medium, low, unknown, malicious, unscanned) and signed."
          required
        >
          <UTextarea v-model="state.condition" :rows="3" class="w-full font-mono" />
        </UFormField>
        <UFormField name="description" label="Description" description="Shown when the gate refuses.">
          <UInput v-model="state.description" class="w-full" />
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
        form="promotion-gate-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
