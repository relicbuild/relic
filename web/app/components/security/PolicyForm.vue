<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import { PolicyKind, type Policy } from '~/gen/relic/v1/scan_pb'

const props = defineProps<{ policy?: Policy }>()
const emit = defineEmits<{ close: [saved: boolean] }>()

const { scans } = useRelic()
const toast = useToast()

const schema = z.object({
  name: z.string().regex(/^[\w.-]+$/, 'Letters, digits, dot, dash and underscore only'),
  kind: z.enum(['security', 'license']),
  description: z.string().optional(),
  rule: z.string().min(1, 'Required'),
  matches: z.enum(['severity', 'malicious', 'secrets', 'liveSecrets', 'misconfigurations', 'endOfLife', 'unmaintained', 'condition']),
  condition: z.string().optional(),
  minSeverity: z.enum(['low', 'medium', 'high', 'critical']),
  fixableOnly: z.boolean(),
  ignoreUnreachable: z.boolean(),
  licenseMode: z.enum(['banned', 'allowed']),
  licenses: z.array(z.string()),
  allowUnknown: z.boolean(),
  failBuild: z.boolean(),
  blockDownload: z.boolean(),
  blockUnscanned: z.boolean()
}).refine(d => d.kind !== 'license' || d.licenses.length > 0, { path: ['licenses'], message: 'Name at least one license' })
  .refine(d => d.kind !== 'security' || d.matches !== 'condition' || !!d.condition?.trim(), { path: ['condition'], message: 'Write the condition' })
type Schema = z.output<typeof schema>

const existing = props.policy?.rules[0]
const state = reactive<Partial<Schema>>({
  name: props.policy?.name ?? '',
  kind: props.policy?.kind === PolicyKind.LICENSE ? 'license' : 'security',
  description: props.policy?.description ?? '',
  rule: existing?.name ?? 'default',
  matches: existing?.malicious
    ? 'malicious'
    : existing?.secrets
      ? 'secrets'
      : existing?.liveSecrets
        ? 'liveSecrets'
        : existing?.misconfigurations
          ? 'misconfigurations'
          : existing?.endOfLife
            ? 'endOfLife'
            : existing?.unmaintained
              ? 'unmaintained'
              : existing?.condition && !existing.minSeverity ? 'condition' : 'severity',
  condition: existing?.condition ?? '',
  minSeverity: (existing?.minSeverity ? severityName(existing.minSeverity) : 'high') as Schema['minSeverity'],
  fixableOnly: existing?.fixVersionOnly ?? false,
  ignoreUnreachable: existing?.ignoreUnreachable ?? false,
  licenseMode: existing?.allowedLicenses.length ? 'allowed' : 'banned',
  licenses: [...(existing?.allowedLicenses.length ? existing.allowedLicenses : existing?.bannedLicenses ?? [])],
  allowUnknown: existing?.allowUnknown ?? true,
  failBuild: existing?.failBuild ?? true,
  blockDownload: existing?.blockDownload ?? false,
  blockUnscanned: existing?.blockUnscanned ?? false
})
const saving = ref(false)

async function onSubmit(event: FormSubmitEvent<Schema>) {
  const d = event.data
  const license = d.kind === 'license'
  const policy = {
    name: d.name,
    kind: license ? PolicyKind.LICENSE : PolicyKind.SECURITY,
    description: d.description,
    rules: [{
      name: d.rule,
      priority: 1,
      ...(license
        ? {
            allowedLicenses: d.licenseMode === 'allowed' ? d.licenses : [],
            bannedLicenses: d.licenseMode === 'banned' ? d.licenses : [],
            allowUnknown: d.allowUnknown
          }
        : {
            malicious: d.matches === 'malicious',
            secrets: d.matches === 'secrets',
            liveSecrets: d.matches === 'liveSecrets',
            misconfigurations: d.matches === 'misconfigurations',
            endOfLife: d.matches === 'endOfLife',
            unmaintained: d.matches === 'unmaintained',
            ...(['severity', 'misconfigurations', 'endOfLife'].includes(d.matches) ? { minSeverity: findingSeverityOf(d.minSeverity) } : {}),
            ...(d.matches === 'severity' ? { fixVersionOnly: d.fixableOnly, ignoreUnreachable: d.ignoreUnreachable } : {}),
            condition: d.condition?.trim() ?? ''
          }),
      failBuild: d.failBuild,
      blockDownload: d.blockDownload,
      blockUnscanned: d.blockDownload && d.blockUnscanned
    }]
  }
  saving.value = true
  try {
    if (!props.policy && (await scans.listPolicies({})).policies.some(p => p.name === d.name)) {
      throw new Error(`policy ${d.name} already exists`)
    }
    await scans.putPolicy({ policy })
    toast.add({ title: `Policy ${d.name} saved`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: 'Could not save the policy', description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover
    :title="policy ? `Edit ${policy.name}` : 'New policy'"
    :description="state.kind === 'license'
      ? 'Components under a banned license, or under none of the allowed ones, violate the policy.'
      : ({
        malicious: 'Packages OpenSSF reports as malicious violate the policy.',
        secrets: 'Secrets found in files (keys, tokens, passwords) violate the policy.',
        liveSecrets: 'Secrets their provider still accepts violate the policy (when the server validates secrets).',
        misconfigurations: 'Misconfigurations in charts, manifests, Terraform and images at or above the minimum severity violate the policy.',
        endOfLife: 'Components and operating systems past end of life (high) or within 90 days of it (low) violate the policy, at or above the minimum severity.',
        unmaintained: 'Deprecated packages, and packages whose source repository shows no activity for 90 days, violate the policy (deps.dev, when the server checks it).',
        severity: 'Vulnerabilities at or above the minimum severity violate the policy.',
        condition: 'Findings and issues the condition holds for violate the policy.'
      })[state.matches ?? 'severity']"
    :close="{ onClick: () => emit('close', false) }"
  >
    <template #body>
      <UForm
        id="policy-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="kind" label="Kind" required>
          <URadioGroup
            v-model="state.kind"
            :disabled="!!policy"
            orientation="horizontal"
            :items="[{ label: 'Security', value: 'security' }, { label: 'License', value: 'license' }]"
          />
        </UFormField>
        <UFormField name="name" label="Name" required>
          <UInput v-model="state.name" :disabled="!!policy" class="w-full" />
        </UFormField>
        <UFormField name="description" label="Description">
          <UInput v-model="state.description" class="w-full" />
        </UFormField>
        <UFormField name="rule" label="Rule name" required>
          <UInput v-model="state.rule" class="w-full" />
        </UFormField>
        <template v-if="state.kind === 'security'">
          <UFormField name="matches" label="Matches" required>
            <URadioGroup
              v-model="state.matches"
              orientation="horizontal"
              :items="[
                { label: 'Vulnerabilities', value: 'severity' },
                { label: 'Malicious packages', value: 'malicious' },
                { label: 'Secrets', value: 'secrets' },
                { label: 'Live secrets', value: 'liveSecrets' },
                { label: 'Misconfigurations', value: 'misconfigurations' },
                { label: 'End of life', value: 'endOfLife' },
                { label: 'Unmaintained', value: 'unmaintained' },
                { label: 'A condition', value: 'condition' }
              ]"
            />
          </UFormField>
          <UFormField
            v-if="state.matches === 'severity' || state.matches === 'misconfigurations' || state.matches === 'endOfLife'"
            name="minSeverity"
            label="Minimum severity"
            required
          >
            <USelect v-model="state.minSeverity" :items="(['low', 'medium', 'high', 'critical'] as const).map(s => ({ label: severityLabel(s), value: s }))" class="w-full" />
          </UFormField>
          <UFormField
            name="condition"
            label="Condition"
            :required="state.matches === 'condition'"
            description="A CEL expression over id, cves, severity, cvss, fixable, malicious, package_name, version, purl, analyzer, rule, file, eol, unmaintained, reachable, secret_live, repository, path and build. It narrows what the rule matches, or alone is what it matches."
          >
            <UTextarea
              v-model="state.condition"
              :rows="2"
              autoresize
              class="w-full font-mono"
              placeholder="severity == &quot;critical&quot; && !purl.startsWith(&quot;pkg:maven/org.example/&quot;)"
            />
          </UFormField>
          <template v-if="state.matches === 'severity'">
            <UFormField name="fixableOnly">
              <UCheckbox v-model="state.fixableOnly" label="Only findings with a fixed version" />
            </UFormField>
            <UFormField name="ignoreUnreachable" description="govulncheck checks Go executables for a call path to the vulnerable functions; every other finding still counts.">
              <UCheckbox v-model="state.ignoreUnreachable" label="Skip Go vulnerabilities no code calls" />
            </UFormField>
          </template>
        </template>
        <template v-else>
          <UFormField name="licenseMode" label="Licenses" required>
            <URadioGroup
              v-model="state.licenseMode"
              orientation="horizontal"
              :items="[{ label: 'Banned', value: 'banned' }, { label: 'Allowed (everything else is banned)', value: 'allowed' }]"
            />
          </UFormField>
          <UFormField name="licenses" description="SPDX identifiers, for example GPL-3.0-only. Press Enter after each.">
            <UInputTags
              v-model="state.licenses"
              add-on-blur
              placeholder="GPL-3.0-only"
              class="w-full"
            />
          </UFormField>
          <UFormField name="allowUnknown">
            <UCheckbox v-model="state.allowUnknown" label="Allow components without a known license" />
          </UFormField>
        </template>
        <UFormField name="failBuild" description="Build scans and dependency audits report the build as failed.">
          <USwitch v-model="state.failBuild" label="Fail builds" />
        </UFormField>
        <UFormField name="blockDownload" description="Downloads of violating artifacts are refused.">
          <USwitch v-model="state.blockDownload" label="Block downloads" />
        </UFormField>
        <UFormField
          v-if="state.blockDownload"
          name="blockUnscanned"
          description="Artifacts not scanned yet, whose scan failed, or that Relic Scan skips, such as files over the scan size limit. Skipped artifacts stay blocked."
        >
          <UCheckbox v-model="state.blockUnscanned" label="Also block artifacts without a successful scan" />
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
        form="policy-form"
        label="Save"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
