<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { WorkloadIdentity } from '~/gen/relic/v1/workload_identity_pb'

const props = defineProps<{
  identity?: WorkloadIdentity
  taken: string[]
  users: string[]
  groups: string[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { workloadIdentities } = useRelic()
const toast = useToast()

const claimLine = /^\s*([^=\s]+)\s*=\s*(\S.*?)\s*$/
const ruleSchema = z.object({
  claims: z.string().refine(text => text.split('\n').filter(l => l.trim()).length > 0 && text.split('\n').filter(l => l.trim()).every(l => claimLine.test(l)), 'One claim=pattern per line, at least one'),
  user: z.string().min(1, 'Choose the user the token is for'),
  groups: z.array(z.string())
})
const schema = z.object({
  name: z.string().regex(/^[a-z0-9][a-z0-9._-]{0,63}$/, 'Lower-case letters, digits, ".", "_" or "-"')
    .refine(n => !!props.identity || !props.taken.includes(n), 'A workload identity with this name exists'),
  enabled: z.boolean(),
  issuer: z.string().regex(/^https?:\/\/\S+$/, 'The issuer URL, for example https://token.actions.githubusercontent.com'),
  audience: z.string().min(1, 'The audience the tokens name'),
  jwks: z.string(),
  tokenLifetimeMinutes: z.number().int().min(0).max(60),
  rules: z.array(ruleSchema).min(1, 'Add a rule')
})
type Schema = z.output<typeof schema>

const id = props.identity
const state = reactive<Schema>({
  name: id?.name ?? '',
  enabled: id?.enabled ?? true,
  issuer: id?.issuer ?? 'https://token.actions.githubusercontent.com',
  audience: id?.audience ?? '',
  jwks: id?.jwks ?? '',
  tokenLifetimeMinutes: id?.tokenLifetimeMinutes || 60,
  rules: id?.rules.map(r => ({
    claims: Object.entries(r.claims).map(([k, v]) => `${k}=${v}`).join('\n'),
    user: r.user,
    groups: [...r.groups]
  })) ?? [{ claims: 'repository=my-org/*', user: '', groups: [] }]
})
const saving = ref(false)
const form = useTemplateRef('form')

function addRule() {
  state.rules.push({ claims: '', user: '', groups: [] })
}

const ruleField = /^rules\.(\d+)\.(.+)$/

function removeRule(index: number) {
  const errors = form.value?.getErrors(ruleField) ?? []
  state.rules.splice(index, 1)
  form.value?.setErrors(errors.flatMap((error) => {
    const [, position, field] = ruleField.exec(error.name ?? '') ?? []
    const rule = Number(position)
    return rule === index ? [] : [{ ...error, name: `rules.${rule > index ? rule - 1 : rule}.${field}` }]
  }), ruleField)
}

function claims(text: string): Record<string, string> {
  return Object.fromEntries(text.split('\n').map(l => claimLine.exec(l)).filter(m => m !== null).map(m => [m[1], m[2]]))
}

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  saving.value = true
  try {
    await workloadIdentities.putWorkloadIdentity({
      workloadIdentity: {
        name: d.name,
        enabled: d.enabled,
        issuer: d.issuer,
        audience: d.audience,
        jwks: d.jwks,
        tokenLifetimeMinutes: d.tokenLifetimeMinutes,
        rules: d.rules.map(r => ({ claims: claims(r.claims), user: r.user, groups: r.groups }))
      }
    })
    toast.add({ title: `Workload identity ${d.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the workload identity', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="identity ? `Edit workload identity ${identity.name}` : 'New workload identity'"
    description="CI jobs exchange OIDC tokens from this issuer for short-lived Relic tokens."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="workload-identity-form"
        ref="form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <div class="grid grid-cols-2 gap-4">
          <UFormField
            name="name"
            label="Name"
            description="The provider name CI passes."
            required
          >
            <UInput v-model="state.name" :disabled="!!identity" class="w-full" />
          </UFormField>
          <UFormField name="tokenLifetimeMinutes" label="Token lifetime (minutes)" description="60 at most.">
            <UInputNumber
              v-model="state.tokenLifetimeMinutes"
              :min="1"
              :max="60"
              class="w-full"
            />
          </UFormField>
        </div>
        <UFormField name="enabled">
          <USwitch v-model="state.enabled" label="Enabled" />
        </UFormField>
        <UFormField name="issuer" label="Issuer" required>
          <UInput v-model="state.issuer" class="w-full font-mono" />
        </UFormField>
        <UFormField
          name="audience"
          label="Audience"
          description="The aud claim the tokens must name."
          required
        >
          <UInput v-model="state.audience" placeholder="relic" class="w-full font-mono" />
        </UFormField>
        <UFormField name="jwks" label="Signing keys (JWKS)" description="Only for an issuer Relic cannot reach; otherwise Relic reads them from the issuer.">
          <UTextarea v-model="state.jwks" :rows="3" class="w-full font-mono" />
        </UFormField>

        <USeparator label="Rules" />
        <p class="text-sm text-muted">
          The first rule whose claims all match a token decides who its Relic token acts as. Patterns are Ant-style:
          <code>*</code> matches within a path segment, <code>**</code> across them.
        </p>

        <UCard v-for="(rule, i) in state.rules" :key="i" variant="subtle">
          <div class="space-y-4">
            <UFormField :name="`rules.${i}.claims`" label="Claims" description="One claim=pattern per line.">
              <UTextarea
                v-model="rule.claims"
                :rows="2"
                placeholder="repository=my-org/*&#10;ref=refs/heads/main"
                class="w-full font-mono"
              />
            </UFormField>
            <div class="grid grid-cols-2 gap-4">
              <UFormField :name="`rules.${i}.user`" label="Acts as user">
                <USelectMenu
                  v-model="rule.user"
                  :items="users"
                  placeholder="Choose a user"
                  class="w-full"
                />
              </UFormField>
              <UFormField :name="`rules.${i}.groups`" label="Narrowed to groups" description="Optional.">
                <USelectMenu
                  v-model="rule.groups"
                  :items="groups"
                  multiple
                  placeholder="All of the user's groups"
                  class="w-full"
                />
              </UFormField>
            </div>
            <UButton
              v-if="state.rules.length > 1"
              icon="i-lucide-trash"
              color="error"
              variant="ghost"
              label="Remove rule"
              @click="removeRule(i)"
            />
          </div>
        </UCard>
        <UFormField name="rules">
          <UButton
            icon="i-lucide-plus"
            color="neutral"
            variant="outline"
            label="Add rule"
            @click="addRule"
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
        form="workload-identity-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
