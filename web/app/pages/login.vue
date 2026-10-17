<script setup lang="ts">
import * as z from 'zod'
import type { AuthFormField, FormSubmitEvent } from '@nuxt/ui'

const route = useRoute()
const { me, refresh, signIn } = useSession()
const { sessions } = useRelic()

const cliSession = computed(() => typeof route.query.cliSession === 'string' ? route.query.cliSession : '')
const cliName = computed(() => typeof route.query.cliName === 'string' ? route.query.cliName : 'A command-line client')

const signInError = ref(typeof route.query.error === 'string' ? route.query.error : '')
const redirect = computed(() => typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/') ? route.query.redirect : '/dashboard')

const { data: signInProviders } = await useAsyncData('session:sign-in-providers', async () => (await sessions.listSignInProviders({})).providers, { default: () => [] })
const { data: signInNote } = await useAsyncData('session:sign-in-note', async () => (await sessions.getSignInNote({})).note, { default: () => '' })
const providers = computed(() => signInProviders.value.map(p => ({
  label: p.displayName,
  icon: 'i-lucide-key-round',
  color: 'neutral' as const,
  variant: 'subtle' as const,
  onClick: () => {
    const back = cliSession.value ? `/login?${new URLSearchParams({ cliSession: cliSession.value, cliName: cliName.value })}` : redirect.value
    return navigateTo(`${p.startPath}?return=${encodeURIComponent(back)}`, { external: true })
  }
})))
const fields: AuthFormField[] = [{
  name: 'username',
  type: 'text',
  label: 'Username',
  placeholder: 'Enter your username',
  required: true
}, {
  name: 'password',
  type: 'password',
  label: 'Password',
  placeholder: 'Enter your password',
  required: true
}]

const signInSchema = z.object({
  username: z.string().min(1, 'Username is required'),
  password: z.string().min(1, 'Password is required')
})
type SignInSchema = z.output<typeof signInSchema>

async function onSignIn(event: FormSubmitEvent<SignInSchema>) {
  signInError.value = ''
  try {
    await signIn(event.data.username, event.data.password)
  } catch (error) {
    signInError.value = relicErrorMessage(error, 'Sign-in failed')
    return
  }
  if (!cliSession.value) {
    await navigateTo(redirect.value)
  }
}

const approveSchema = z.object({
  code: z.array(z.string()).length(4, 'Enter all 4 characters shown in your terminal')
})
type ApproveSchema = z.output<typeof approveSchema>

const approveState = reactive<Partial<ApproveSchema>>({ code: [] })
const approving = ref(false)
const approved = ref(false)
const approveError = ref('')

async function onApprove(event: FormSubmitEvent<ApproveSchema>) {
  approveError.value = ''
  approving.value = true
  try {
    await sessions.approveLogin({ session: cliSession.value, code: event.data.code.join('') })
    approved.value = true
    await navigateTo({ query: { ...route.query, cliSession: undefined } }, { replace: true })
  } catch (error) {
    approveError.value = relicErrorMessage(error, 'Approval failed')
  } finally {
    approving.value = false
  }
}

onMounted(() => refresh().catch(() => null))
</script>

<template>
  <UMain class="flex items-center justify-center p-4">
    <UPageCard v-if="!me" class="w-full max-w-md">
      <UAuthForm
        :schema="signInSchema"
        :fields="fields"
        :providers="providers"
        separator="or"
        title="Sign in to Relic"
        :description="cliSession ? `${cliName} is asking to sign in. Sign in first to approve it.` : undefined"
        icon="i-lucide-package"
        :submit="{ label: 'Sign in', block: true }"
        @submit="onSignIn"
      >
        <template v-if="signInNote" #footer>
          <UAlert
            color="info"
            variant="subtle"
            icon="i-lucide-info"
            :description="signInNote"
            :ui="{ description: 'whitespace-pre-line text-start' }"
          />
        </template>
        <template #validation>
          <UAlert
            v-if="signInError"
            color="error"
            icon="i-lucide-circle-alert"
            :title="signInError"
          />
        </template>
      </UAuthForm>
    </UPageCard>

    <UPageCard
      v-else-if="cliSession && !approved"
      class="w-full max-w-md"
      icon="i-lucide-terminal"
      :title="`Approve ${cliName} sign-in`"
      :description="`Signed in as ${me.username}. Enter the 4-character code shown in your terminal. Only approve a request you started yourself.`"
    >
      <UForm
        :schema="approveSchema"
        :state="approveState"
        class="flex flex-col items-center gap-4"
        @submit="onApprove"
      >
        <UFormField name="code" :error="approveError || undefined">
          <UPinInput
            v-model="approveState.code"
            :length="4"
            otp
            autofocus
            aria-label="Verification code"
          />
        </UFormField>

        <UButton
          type="submit"
          label="Approve"
          block
          :loading="approving"
        />
      </UForm>
    </UPageCard>

    <UPageCard
      v-else-if="approved"
      class="w-full max-w-md"
      icon="i-lucide-circle-check"
      :title="`${cliName} is signed in`"
      description="You can close this page and return to your terminal."
      :ui="{ leadingIcon: 'text-success' }"
    />

    <UPageCard
      v-else
      class="w-full max-w-md"
      icon="i-lucide-user-check"
      title="Signed in"
      :description="`Signed in as ${me.username}.`"
    >
      <UButton to="/dashboard" label="Continue" block />
    </UPageCard>
  </UMain>
</template>
