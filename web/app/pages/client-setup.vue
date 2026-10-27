<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent, TableColumn } from '@nuxt/ui'
import type { AccessToken } from '~/gen/relic/v1/security_pb'
import { ConfirmModal } from '#components'

definePageMeta({ layout: 'dashboard' })

const relic = useRelic()
const secondsPerDay = 86_400n
const toast = useToast()
const { me } = useSession()
const { data: repos, error: reposError, refresh: refreshRepos } = await useAsyncData('client-setup:repositories', async () => (await relic.repositories.listRepositories({})).repositories)

const selected = ref<string>()
const repo = computed(() => repos.value?.find(r => r.name === selected.value))

const expiry = computed(() => tokenExpiry(!!me.value?.admin))
const tokenSchema = computed(() => z.object({
  description: z.string().min(1, 'Say what the token is for'),
  expiresDays: z.number().int().min(expiry.value.minDays).max(expiry.value.maxDays, `At most ${expiry.value.maxDays} days`)
}))
type TokenSchema = z.output<typeof tokenSchema.value>
const tokenState = reactive<Partial<TokenSchema>>({ description: '', expiresDays: 90 })
const token = ref('')
const creating = ref(false)

async function createToken(event: FormSubmitEvent<TokenSchema>) {
  creating.value = true
  try {
    const res = await relic.security.createToken({ description: event.data.description, expiresInSeconds: BigInt(event.data.expiresDays) * secondsPerDay })
    token.value = res.accessToken
    await refreshTokens()
  } catch (err) {
    toast.add({ title: 'Could not create a token', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    creating.value = false
  }
}

const { data: endpoints, status: endpointsStatus, error: endpointsError, refresh: refreshEndpoints } = await useAsyncData('client-endpoints', async () =>
  selected.value ? await relic.repositories.getClientEndpoints({ name: selected.value }) : undefined, { watch: [selected] })

const snippets = computed(() => repo.value && endpoints.value && endpointsStatus.value === 'success'
  ? clientSetupSnippets(repo.value, endpoints.value, me.value?.username ?? '<user>', token.value || '<token>')
  : [])

const copy = useCopy()

const confirm = useOverlay().create(ConfirmModal)
const { data: tokens, status: tokensStatus, error: tokensError, refresh: refreshTokens } = await useAsyncData('client-setup:tokens', async () =>
  (await relic.security.listTokens({ username: me.value?.username ?? '' })).tokens)
const tokenColumns: TableColumn<AccessToken>[] = [
  { accessorKey: 'description', header: 'Description' },
  { id: 'created', header: 'Created' },
  { id: 'expires', header: 'Expires' },
  { id: 'actions' }
]

async function revoke(t: AccessToken) {
  if (!(await confirm.open({ title: `Revoke the token ${t.description || t.id}?`, description: 'Clients using it are refused from now on.', confirmLabel: 'Revoke' }).result)) {
    return
  }
  try {
    await relic.security.revokeToken({ id: t.id })
    toast.add({ title: 'Token revoked', color: 'success', icon: 'i-lucide-check' })
    await refreshTokens()
  } catch (err) {
    toast.add({ title: 'Could not revoke the token', description: relicErrorMessage(err, ''), color: 'error' })
  }
}
</script>

<template>
  <UDashboardPanel id="setup">
    <template #header>
      <UDashboardNavbar title="Set me up">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div class="flex flex-col gap-8">
        <UPageCard title="Access token" description="Tokens authenticate package managers and scripts as you. A token is shown only once." variant="subtle">
          <UForm
            v-if="!token"
            :schema="tokenSchema"
            :state="tokenState"
            class="flex flex-wrap items-end gap-4"
            @submit="createToken"
          >
            <UFormField name="description" label="Description" class="min-w-64 flex-1">
              <UInput v-model="tokenState.description" placeholder="Laptop, CI pipeline..." class="w-full" />
            </UFormField>
            <UFormField name="expiresDays" :label="expiry.label">
              <UInputNumber v-model="tokenState.expiresDays" :min="expiry.minDays" :max="expiry.maxDays" />
            </UFormField>
            <UButton type="submit" label="Generate token" :loading="creating" />
          </UForm>
          <div v-else class="flex flex-col gap-3">
            <UAlert
              color="warning"
              variant="subtle"
              icon="i-lucide-key-round"
              title="Copy this token now: it will not be shown again."
            />
            <UFieldGroup class="w-full">
              <UInput :model-value="token" readonly class="w-full font-mono" />
              <UButton
                icon="i-lucide-copy"
                color="neutral"
                variant="outline"
                aria-label="Copy token"
                @click="copy(token)"
              />
            </UFieldGroup>
            <div>
              <UButton
                label="Done"
                color="neutral"
                variant="outline"
                @click="token = ''; tokenState.description = ''"
              />
            </div>
          </div>
        </UPageCard>

        <UPageCard title="Your access tokens" description="Revoke a token that leaked or is no longer used." variant="subtle">
          <LoadError :error="tokensError" what="your tokens" @retry="refreshTokens()" />
          <UTable
            :data="tokens ?? []"
            :columns="tokenColumns"
            :loading="tokensStatus === 'pending'"
            :ui="cardTable"
          >
            <template #description-cell="{ row }">
              <span class="text-highlighted">{{ row.original.description || row.original.id }}</span>
            </template>
            <template #created-cell="{ row }">
              {{ formatDate(toIsoDate(row.original.created)) }}
            </template>
            <template #expires-cell="{ row }">
              {{ row.original.expires ? formatDate(toIsoDate(row.original.expires)) : 'Never' }}
            </template>
            <template #actions-cell="{ row }">
              <div class="flex justify-end">
                <UButton
                  icon="i-lucide-ban"
                  color="error"
                  variant="ghost"
                  :aria-label="`Revoke ${row.original.description || row.original.id}`"
                  @click="revoke(row.original)"
                />
              </div>
            </template>
            <template #empty>
              <UEmpty
                variant="naked"
                icon="i-lucide-key-round"
                title="No tokens"
                description="Tokens you generate appear here."
              />
            </template>
          </UTable>
        </UPageCard>

        <UPageCard title="Client configuration" description="Choose a repository to get the commands for its package type." variant="subtle">
          <LoadError :error="reposError" what="repositories" @retry="refreshRepos()" />
          <LoadError :error="endpointsError" :what="`the endpoints of ${selected}`" @retry="refreshEndpoints()" />
          <USelectMenu
            v-model="selected"
            :items="(repos ?? []).map(r => ({ label: `${r.name} (${formatName(r.format)}, ${repositoryClassName(r.class)})`, value: r.name, icon: formatIcon(formatName(r.format)) }))"
            value-key="value"
            placeholder="Repository"
            class="w-full sm:w-96"
          />
          <div v-for="s in snippets" :key="s.title" class="flex min-w-0 flex-col gap-2">
            <div class="flex items-center justify-between">
              <span class="text-sm font-medium text-highlighted">{{ s.title }}</span>
              <UButton
                icon="i-lucide-copy"
                size="xs"
                color="neutral"
                variant="ghost"
                :aria-label="`Copy ${s.title}`"
                @click="copy(s.code)"
              />
            </div>
            <pre class="overflow-x-auto rounded-md bg-elevated p-3 font-mono text-sm text-default">{{ s.code }}</pre>
          </div>
        </UPageCard>
      </div>
    </template>
  </UDashboardPanel>
</template>
