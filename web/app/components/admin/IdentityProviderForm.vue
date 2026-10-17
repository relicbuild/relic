<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { IdentityProvider } from '~/gen/relic/v1/identity_provider_pb'

const props = defineProps<{
  provider?: IdentityProvider
  taken: string[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { identityProviders } = useRelic()
const toast = useToast()

const schema = z.object({
  name: z.string().regex(/^[a-z0-9][a-z0-9._-]{0,63}$/, 'Lower-case letters, digits, ".", "_" or "-"')
    .refine(n => !!props.provider || !props.taken.includes(n), 'An identity provider with this name exists'),
  displayName: z.string(),
  enabled: z.boolean(),
  protocol: z.enum(['oidc', 'saml']),
  issuer: z.string(),
  clientId: z.string(),
  clientSecret: z.string(),
  scopes: z.string(),
  metadataUrl: z.string(),
  metadataXml: z.string(),
  autoCreateUsers: z.boolean(),
  usernameClaim: z.string(),
  groupsClaim: z.string()
}).refine(s => s.protocol !== 'oidc' || /^https?:\/\/\S+$/.test(s.issuer), { message: 'The issuer URL, for example https://login.example.com', path: ['issuer'] })
  .refine(s => s.protocol !== 'oidc' || s.clientId !== '', { message: 'The client ID the provider gave Relic', path: ['clientId'] })
  .refine(s => s.protocol !== 'saml' || s.metadataUrl !== '' || s.metadataXml !== '', { message: 'The metadata URL, or paste the metadata XML', path: ['metadataUrl'] })
type Schema = z.output<typeof schema>

const p = props.provider
const oidc = p?.protocol.case === 'oidc' ? p.protocol.value : undefined
const saml = p?.protocol.case === 'saml' ? p.protocol.value : undefined
const state = reactive<Schema>({
  name: p?.name ?? '',
  displayName: p?.displayName ?? '',
  enabled: p?.enabled ?? true,
  protocol: saml ? 'saml' : 'oidc',
  issuer: oidc?.issuer ?? '',
  clientId: oidc?.clientId ?? '',
  clientSecret: '',
  scopes: oidc?.scopes.join(' ') ?? '',
  metadataUrl: saml?.metadataUrl ?? '',
  metadataXml: saml?.metadataXml ?? '',
  autoCreateUsers: p?.autoCreateUsers ?? false,
  usernameClaim: p?.usernameClaim ?? '',
  groupsClaim: p?.groupsClaim ?? ''
})
const protocols = [
  { label: 'OpenID Connect', value: 'oidc', description: 'Entra ID, Okta, Keycloak, Google, Auth0' },
  { label: 'SAML 2.0', value: 'saml', description: 'ADFS, Okta, Entra ID, Shibboleth' }
]
const providerUrl = computed(() => `${useRequestURL().origin}/api/v1/sso/${state.name || '<name>'}`)
const callbackUrl = computed(() => `${providerUrl.value}/${state.protocol === 'oidc' ? 'callback' : 'acs'}`)
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  saving.value = true
  try {
    await identityProviders.putIdentityProvider({
      identityProvider: {
        name: d.name,
        displayName: d.displayName,
        enabled: d.enabled,
        autoCreateUsers: d.autoCreateUsers,
        usernameClaim: d.usernameClaim,
        groupsClaim: d.groupsClaim,
        protocol: d.protocol === 'oidc'
          ? { case: 'oidc', value: { issuer: d.issuer, clientId: d.clientId, clientSecret: d.clientSecret, scopes: d.scopes.split(/\s+/).filter(Boolean) } }
          : { case: 'saml', value: { metadataUrl: d.metadataUrl, metadataXml: d.metadataXml } }
      }
    })
    toast.add({ title: `Identity provider ${d.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the identity provider', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="provider ? `Edit identity provider ${provider.name}` : 'New identity provider'"
    description="Users sign in with their account at this identity provider."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="identity-provider-form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <div class="grid grid-cols-2 gap-4">
          <UFormField name="name" label="Name" required>
            <UInput v-model="state.name" :disabled="!!provider" class="w-full" />
          </UFormField>
          <UFormField name="displayName" label="Button label" description="On the sign-in page.">
            <UInput v-model="state.displayName" :placeholder="state.name" class="w-full" />
          </UFormField>
        </div>
        <UFormField name="enabled">
          <USwitch v-model="state.enabled" label="Enabled" />
        </UFormField>
        <UFormField name="protocol" label="Protocol">
          <URadioGroup
            v-model="state.protocol"
            :items="protocols"
            :disabled="!!provider"
            variant="card"
            orientation="horizontal"
          />
        </UFormField>
        <UFormField
          :label="state.protocol === 'oidc' ? 'Redirect URI' : 'Assertion consumer service URL'"
          description="Register this URL with the identity provider."
        >
          <UInput :model-value="callbackUrl" readonly class="w-full font-mono" />
        </UFormField>

        <template v-if="state.protocol === 'oidc'">
          <UFormField name="issuer" label="Issuer URL" required>
            <UInput v-model="state.issuer" placeholder="https://login.example.com" class="w-full font-mono" />
          </UFormField>
          <div class="grid grid-cols-2 items-end gap-4">
            <UFormField name="clientId" label="Client ID" required>
              <UInput v-model="state.clientId" autocomplete="off" class="w-full font-mono" />
            </UFormField>
            <UFormField name="clientSecret" label="Client secret" :hint="provider ? 'Leave empty to keep' : undefined">
              <UInput
                v-model="state.clientSecret"
                type="password"
                autocomplete="new-password"
                class="w-full"
              />
            </UFormField>
          </div>
          <UFormField name="scopes" label="Scopes" description="Separated by spaces; openid, profile and email when empty.">
            <UInput v-model="state.scopes" placeholder="openid profile email groups" class="w-full font-mono" />
          </UFormField>
        </template>
        <template v-else>
          <UFormField label="Relic's metadata URL" description="Give the identity provider this URL, or the metadata it serves.">
            <UInput :model-value="`${providerUrl}/metadata`" readonly class="w-full font-mono" />
          </UFormField>
          <UFormField name="metadataUrl" label="Metadata URL" description="Where Relic reads the identity provider's metadata.">
            <UInput v-model="state.metadataUrl" placeholder="https://idp.example.com/metadata" class="w-full font-mono" />
          </UFormField>
          <UFormField name="metadataXml" label="Metadata XML" description="Or paste the metadata, for a provider Relic cannot reach.">
            <UTextarea v-model="state.metadataXml" :rows="4" class="w-full font-mono" />
          </UFormField>
        </template>

        <USeparator label="Accounts and groups" />

        <UFormField name="autoCreateUsers">
          <USwitch
            v-model="state.autoCreateUsers"
            label="Create users on their first sign-in"
            description="Otherwise only users that exist in Relic, created by SCIM or an administrator, can sign in."
          />
        </UFormField>
        <div class="grid grid-cols-2 gap-4">
          <UFormField
            name="usernameClaim"
            :label="state.protocol === 'oidc' ? 'Username claim' : 'Username attribute'"
            :description="state.protocol === 'oidc' ? 'preferred_username when empty.' : 'The NameID when empty.'"
          >
            <UInput v-model="state.usernameClaim" class="w-full font-mono" />
          </UFormField>
          <UFormField
            name="groupsClaim"
            :label="state.protocol === 'oidc' ? 'Groups claim' : 'Groups attribute'"
            description="Members join the Relic groups it names."
          >
            <UInput v-model="state.groupsClaim" placeholder="groups" class="w-full font-mono" />
          </UFormField>
        </div>
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
        form="identity-provider-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
