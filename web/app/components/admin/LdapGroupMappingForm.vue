<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import { GroupStrategy, type LdapGroupMapping } from '~/gen/relic/v1/ldap_pb'

const props = defineProps<{
  groupMapping?: LdapGroupMapping
  ldapServers: string[]
  taken: string[]
}>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const { ldap } = useRelic()
const toast = useToast()

const strategies = [
  { label: 'Static', value: 'static', description: 'Groups list their members (member, uniqueMember).' },
  { label: 'Dynamic', value: 'dynamic', description: 'Users list their groups (memberOf).' },
  { label: 'Hierarchy', value: 'hierarchy', description: 'The organizational units in the user\'s DN.' }
] as const

const schema = z.object({
  name: z.string().regex(/^[\w.-]{1,64}$/, 'Letters, digits, "-", "_" or "."')
    .refine(n => !!props.groupMapping || !props.taken.includes(n), 'An LDAP group mapping with this name exists'),
  ldapServer: z.string().min(1, 'Choose an LDAP server'),
  strategy: z.enum(['static', 'dynamic', 'hierarchy']),
  baseDn: z.string(),
  nameAttribute: z.string().min(1, 'Required'),
  memberAttribute: z.string(),
  filter: z.string(),
  subTree: z.boolean(),
  descriptionAttribute: z.string()
}).refine(g => g.strategy === 'hierarchy' || g.memberAttribute !== '', {
  message: 'Required for this strategy',
  path: ['memberAttribute']
})
type Schema = z.output<typeof schema>

const g = props.groupMapping
const state = reactive<Schema>({
  name: g?.name ?? '',
  ldapServer: g?.ldapServer ?? props.ldapServers[0] ?? '',
  strategy: (enumName(GroupStrategy, g?.strategy ?? GroupStrategy.UNSPECIFIED) || 'static') as Schema['strategy'],
  baseDn: g?.baseDn ?? 'ou=groups',
  nameAttribute: g?.nameAttribute ?? 'cn',
  memberAttribute: g?.memberAttribute ?? 'uniqueMember',
  filter: g?.filter ?? '(objectClass=groupOfUniqueNames)',
  subTree: g?.subTree ?? true,
  descriptionAttribute: g?.descriptionAttribute ?? 'description'
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    if (!props.groupMapping && (await ldap.listLdapGroupMappings({})).ldapGroupMappings.some(m => m.name === event.data.name)) {
      throw new Error(`LDAP group mapping ${event.data.name} already exists`)
    }
    const filter = event.data.strategy === 'static' ? event.data.filter : ''
    await ldap.putLdapGroupMapping({ ldapGroupMapping: { ...event.data, filter, strategy: enumValue(GroupStrategy, event.data.strategy) } })
    toast.add({ title: `LDAP group mapping ${event.data.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the LDAP group mapping', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="groupMapping ? `Edit LDAP group mapping ${groupMapping.name}` : 'New LDAP group mapping'"
    description="On each sign-in, users join the Relic groups named like their LDAP groups."
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="ldap-group-mapping-form"
        :schema="schema"
        :state="state"
        class="space-y-5"
        @submit="onSubmit"
      >
        <div class="grid grid-cols-2 gap-4">
          <UFormField name="name" label="Name" required>
            <UInput v-model="state.name" :disabled="!!groupMapping" class="w-full" />
          </UFormField>
          <UFormField name="ldapServer" label="LDAP server" required>
            <USelect v-model="state.ldapServer" :items="ldapServers" class="w-full" />
          </UFormField>
        </div>
        <UFormField name="strategy" label="Strategy">
          <URadioGroup v-model="state.strategy" :items="[...strategies]" />
        </UFormField>
        <template v-if="state.strategy !== 'hierarchy'">
          <UFormField
            v-if="state.strategy === 'static'"
            name="baseDn"
            label="Group base DN"
            hint="Relative to the base DN"
          >
            <UInput v-model="state.baseDn" class="w-full font-mono" />
          </UFormField>
          <div class="grid grid-cols-2 gap-4">
            <UFormField name="nameAttribute" label="Group name attribute" required>
              <UInput v-model="state.nameAttribute" class="w-full font-mono" />
            </UFormField>
            <UFormField
              name="memberAttribute"
              :label="state.strategy === 'static' ? 'Member attribute' : 'User\'s group attribute'"
              required
            >
              <UInput v-model="state.memberAttribute" class="w-full font-mono" />
            </UFormField>
          </div>
          <template v-if="state.strategy === 'static'">
            <UFormField name="filter" label="Filter">
              <UInput v-model="state.filter" class="w-full font-mono" />
            </UFormField>
            <UFormField name="subTree">
              <USwitch v-model="state.subTree" label="Search the whole subtree" />
            </UFormField>
          </template>
        </template>
        <UFormField
          v-else
          name="nameAttribute"
          label="Unit attribute"
          description="The attribute naming each unit, usually ou."
          required
        >
          <UInput v-model="state.nameAttribute" class="w-full font-mono" />
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
        form="ldap-group-mapping-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
