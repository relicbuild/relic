<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { Waiver } from '~/gen/relic/v1/waiver_pb'

const props = defineProps<{ waiver: Waiver, approve: boolean }>()
const emit = defineEmits<{ close: [decided: boolean] }>()
const { waivers } = useRelic()
const toast = useToast()

const schema = z.object({
  days: z.number().int().min(1, 'At least a day').max(365, 'At most a year'),
  note: z.string()
})
type Schema = z.output<typeof schema>

const state = reactive<Schema>({ days: 30, note: '' })
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    await (props.approve
      ? waivers.approveWaiver({ id: props.waiver.id, days: event.data.days, note: event.data.note })
      : waivers.rejectWaiver({ id: props.waiver.id, note: event.data.note }))
    toast.add({ title: props.approve ? 'Waiver approved' : 'Waiver rejected', color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not decide', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <UModal
    :title="approve ? 'Approve the waiver' : 'Reject the waiver'"
    :description="`${waiver.requestedBy}: ${waiver.reason}`"
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="waiver-decision-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField
          v-if="approve"
          name="days"
          label="Days"
          description="The ignore rule expires after them."
        >
          <UInputNumber v-model="state.days" :min="1" :max="365" />
        </UFormField>
        <UFormField name="note" label="Note" hint="Optional">
          <UTextarea v-model="state.note" autoresize class="w-full" />
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
        form="waiver-decision-form"
        :color="approve ? 'primary' : 'error'"
        :label="approve ? 'Approve' : 'Reject'"
        :loading="saving"
      />
    </template>
  </UModal>
</template>
