<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { LdapServer, TestLdapSignInResponse } from '~/gen/relic/v1/ldap_pb'

const props = defineProps<{
  server?: LdapServer
  taken: string[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { ldap } = useRelic()
const toast = useToast()

const schema = z.object({
  name: z.string().regex(/^[\w.-]{1,64}$/, 'Letters, digits, "-", "_" or "."')
    .refine(k => !!props.server || !props.taken.includes(k), 'An LDAP server with this name exists'),
  enabled: z.boolean(),
  url: z.string().regex(/^ldaps?:\/\/[^/\s]+(\/.*)?$/, 'ldap://host[:port]/<base DN> or ldaps://…'),
  startTls: z.boolean(),
  userDnPattern: z.string(),
  search: z.object({
    filter: z.string(),
    base: z.string(),
    subTree: z.boolean(),
    managerDn: z.string(),
    managerPassword: z.string()
  }),
  autoCreateUser: z.boolean(),
  emailAttribute: z.string()
}).refine(s => s.userDnPattern.includes('{0}') || s.search.filter.includes('{0}'), {
  message: 'Set a user DN pattern or a search filter, with {0} for the user name',
  path: ['userDnPattern']
})
type Schema = z.output<typeof schema>

const s = props.server
const state = reactive<Schema>({
  name: s?.name ?? '',
  enabled: s?.enabled ?? true,
  url: s?.url ?? '',
  startTls: s?.startTls ?? false,
  userDnPattern: s?.userDnPattern ?? '',
  search: {
    filter: s?.search?.filter ?? 'uid={0}',
    base: s?.search?.base ?? '',
    subTree: s?.search?.subTree ?? true,
    managerDn: s?.search?.managerDn ?? '',
    managerPassword: ''
  },
  autoCreateUser: s?.autoCreateUser ?? true,
  emailAttribute: s?.emailAttribute ?? 'mail'
})
const saving = ref(false)

const body = () => ({ ...state, search: { ...state.search } })

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    await ldap.putLdapServer({ ldapServer: body() })
    toast.add({ title: `LDAP server ${event.data.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the LDAP server', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}

const tester = reactive({ username: '', password: '' })
const testing = ref(false)
const result = ref<Pick<TestLdapSignInResponse, 'ok' | 'dn' | 'email' | 'groups' | 'error'>>()

async function test() {
  testing.value = true
  result.value = undefined
  try {
    result.value = await ldap.testLdapSignIn({ ldapServer: body(), ...tester })
  } catch (err) {
    result.value = { ok: false, dn: '', email: '', groups: [], error: relicErrorMessage(err, 'The test could not run.') }
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <USlideover
    :title="server ? `Edit LDAP server ${server.name}` : 'New LDAP server'"
    description="Users not known to Relic sign in against this LDAP server."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="ldap-server-form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <div class="grid grid-cols-2 gap-4">
          <UFormField name="name" label="Name" required>
            <UInput v-model="state.name" :disabled="!!server" class="w-full" />
          </UFormField>
          <UFormField name="emailAttribute" label="Email attribute">
            <UInput v-model="state.emailAttribute" class="w-full" />
          </UFormField>
        </div>
        <UFormField
          name="url"
          label="LDAP URL"
          description="The server and the base DN, for example ldap://ldap.example.com:389/dc=example,dc=com."
          required
        >
          <UInput v-model="state.url" class="w-full font-mono" />
        </UFormField>
        <UFormField name="startTls">
          <USwitch
            v-model="state.startTls"
            label="Use StartTLS"
            description="Upgrade an ldap:// connection to TLS. ldaps:// URLs always use TLS."
          />
        </UFormField>

        <USeparator label="Finding users" />

        <UFormField
          name="userDnPattern"
          label="User DN pattern"
          description="Relative to the base DN; {0} is the user name. Leave empty to search instead."
        >
          <UInput v-model="state.userDnPattern" placeholder="uid={0},ou=people" class="w-full font-mono" />
        </UFormField>
        <div class="grid grid-cols-2 gap-4">
          <UFormField name="search.filter" label="Search filter" description="{0} is the user name.">
            <UInput v-model="state.search.filter" class="w-full font-mono" />
          </UFormField>
          <UFormField name="search.base" label="Search base" description="Relative to the base DN.">
            <UInput v-model="state.search.base" placeholder="ou=people" class="w-full font-mono" />
          </UFormField>
        </div>
        <UFormField name="search.subTree">
          <USwitch v-model="state.search.subTree" label="Search the whole subtree" />
        </UFormField>
        <div class="grid grid-cols-2 items-end gap-4">
          <UFormField name="search.managerDn" label="Manager DN" description="Used for searches and group lookups.">
            <UInput v-model="state.search.managerDn" autocomplete="off" class="w-full font-mono" />
          </UFormField>
          <UFormField name="search.managerPassword" label="Manager password" :hint="server?.hasManagerPassword ? 'Leave empty to keep' : undefined">
            <UInput
              v-model="state.search.managerPassword"
              type="password"
              autocomplete="new-password"
              class="w-full"
            />
          </UFormField>
        </div>

        <USeparator />

        <UFormField name="enabled">
          <USwitch v-model="state.enabled" label="Enabled" />
        </UFormField>
        <UFormField name="autoCreateUser">
          <USwitch
            v-model="state.autoCreateUser"
            label="Create users on first sign-in"
            description="Otherwise only LDAP users who already have a Relic account can sign in."
          />
        </UFormField>
      </UForm>

      <UCard class="mt-6" variant="subtle">
        <template #header>
          <p class="font-medium text-highlighted">
            Test sign-in
          </p>
          <p class="text-sm text-muted">
            Tries the settings above, saved or not, with an LDAP user's credentials.
          </p>
        </template>
        <div class="grid grid-cols-2 gap-4">
          <UFormField label="User name">
            <UInput v-model="tester.username" autocomplete="off" class="w-full" />
          </UFormField>
          <UFormField label="Password">
            <UInput
              v-model="tester.password"
              type="password"
              autocomplete="new-password"
              class="w-full"
            />
          </UFormField>
        </div>
        <UButton
          class="mt-4"
          icon="i-lucide-plug-zap"
          label="Test"
          color="neutral"
          variant="outline"
          :loading="testing"
          :disabled="!tester.username || !tester.password"
          @click="test"
        />
        <UAlert
          v-if="result"
          class="mt-4"
          :color="result.ok ? 'success' : 'error'"
          variant="subtle"
          :icon="result.ok ? 'i-lucide-circle-check' : 'i-lucide-circle-x'"
          :title="result.ok ? 'Signed in' : 'Sign-in failed'"
          :description="result.ok ? `${result.dn}${result.email ? ` · ${result.email}` : ''}` : result.error"
        >
          <template v-if="result.ok" #actions>
            <div class="flex flex-wrap gap-1">
              <UBadge
                v-for="g in result.groups ?? []"
                :key="g"
                :label="g"
                color="neutral"
                variant="outline"
              />
              <span v-if="!result.groups?.length" class="text-xs text-muted">No groups</span>
            </div>
          </template>
        </UAlert>
      </UCard>
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
        form="ldap-server-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
