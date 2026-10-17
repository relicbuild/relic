<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'
import type { RepositoryRow } from '~/utils/view-models'
import { create } from '@bufbuild/protobuf'
import { RemoteRepositorySettingsSchema, VirtualRepositorySettingsSchema, type Repository } from '~/gen/relic/v1/repository_pb'

const props = defineProps<{ repositories: RepositoryRow[], repository?: Repository }>()
const emit = defineEmits<{ close: [saved: boolean] }>()
const relic = useRelic()
const toast = useToast()

const defaultUpstream: Record<string, string> = {
  maven: 'https://repo1.maven.org/maven2',
  gradle: 'https://repo1.maven.org/maven2',
  npm: 'https://registry.npmjs.org',
  pypi: 'https://files.pythonhosted.org',
  gomod: 'https://proxy.golang.org',
  docker: 'https://registry-1.docker.io',
  nuget: 'https://www.nuget.org',
  rubygems: 'https://rubygems.org',
  cargo: 'https://index.crates.io',
  conan: 'https://center2.conan.io',
  huggingface: 'https://huggingface.co',
  alpine: 'https://dl-cdn.alpinelinux.org/alpine',
  debian: 'https://deb.debian.org/debian'
}

const schema = z.object({
  name: z.string().regex(/^[a-zA-Z0-9][\w.-]{0,63}$/, 'Letters, digits, dot, dash and underscore; up to 64'),
  class: z.enum(['local', 'remote', 'virtual']),
  format: z.string(),
  description: z.string().optional(),
  url: z.string().optional(),
  username: z.string().optional(),
  password: z.string().optional(),
  indexUrl: z.union([z.literal(''), z.url({ protocol: /^https?$/, error: 'An http(s) URL' })]).optional(),
  offline: z.boolean(),
  disableCache: z.boolean(),
  cachePeriodSeconds: z.number().int().min(0),
  missedRetrievalSeconds: z.number().int().min(0),
  cooldownDays: z.number().int().min(0).max(365),
  quarantine: z.boolean(),
  timeoutMilliseconds: z.number().int().min(0),
  maxUploadSizeMiB: z.number().int().min(0),
  maxStorageGiB: z.number().int().min(0),
  immutable: z.boolean(),
  repositories: z.array(z.string()),
  localOnlyPatterns: z.array(z.string()),
  defaultDeploymentRepo: z.string().optional(),
  signingKey: z.string().optional(),
  metadataDepth: z.number().int().min(0).max(16),
  architectures: z.array(z.string())
}).superRefine((d, ctx) => {
  if (d.class === 'remote' && !/^https?:\/\/.+/.test(d.url ?? '')) {
    ctx.addIssue({ code: 'custom', path: ['url'], message: 'An http(s) URL is required' })
  }
  if (d.class === 'virtual' && d.repositories.length === 0) {
    ctx.addIssue({ code: 'custom', path: ['repositories'], message: 'Choose at least one member' })
  }
})
type Schema = z.output<typeof schema>

const MiB = 1048576n
const GiB = 1073741824n
const r = props.repository
const state = reactive<Partial<Schema>>({
  name: r?.name ?? '',
  class: r ? repositoryClassName(r.class) || 'local' : 'local',
  format: r ? formatName(r.format) || 'generic' : 'generic',
  description: r?.description ?? '',
  url: r?.remote?.url ?? '',
  username: r?.remote?.username ?? '',
  password: '',
  indexUrl: r?.remote?.indexUrl ?? '',
  offline: r?.remote?.offline ?? false,
  disableCache: r?.remote?.disableCache ?? false,
  cachePeriodSeconds: Number(r?.remote?.cachePeriodSeconds ?? 0),
  missedRetrievalSeconds: Number(r?.remote?.missedRetrievalCachePeriodSeconds ?? 1800),
  cooldownDays: r?.remote?.cooldownDays ?? 0,
  quarantine: r?.remote?.quarantine ?? false,
  timeoutMilliseconds: Number(r?.remote?.timeoutMilliseconds ?? 0),
  maxUploadSizeMiB: sizeInUnits(r?.maxUploadSize ?? 0n, MiB),
  maxStorageGiB: sizeInUnits(r?.maxStorage ?? 0n, GiB),
  immutable: r?.immutable ?? false,
  repositories: [...(r?.virtual?.members ?? [])],
  localOnlyPatterns: [...(r?.virtual?.localOnlyPatterns ?? [])],
  defaultDeploymentRepo: r?.virtual?.deployTo ?? '',
  signingKey: r?.signingKey ?? '',
  metadataDepth: r?.metadataDepth ?? 0,
  architectures: [...(r?.architectures ?? [])]
})
const defaultSigningKey: Record<string, string> = {
  alpine: 'relic-rsa',
  debian: 'relic-gpg',
  rpm: 'relic-gpg'
}
const signsIndexes = computed(() => !!defaultSigningKey[state.format ?? ''] && state.class !== 'remote')
watch(() => [state.class, state.format], () => {
  if (state.class === 'remote' && !state.url) {
    state.url = defaultUpstream[state.format ?? ''] ?? ''
  }
})

const members = computed(() => props.repositories
  .filter(r => r.class !== 'virtual' || r.name !== state.name)
  .filter(r => r.format === state.format)
  .map(r => r.name))
const locals = computed(() => (state.repositories ?? []).filter(n => props.repositories.find(r => r.name === n)?.class === 'local'))
watch(locals, (names) => {
  if (state.defaultDeploymentRepo && !names.includes(state.defaultDeploymentRepo)) {
    state.defaultDeploymentRepo = ''
  }
})
const saving = ref(false)
async function onSubmit(event: FormSubmitEvent<Schema>) {
  saving.value = true
  try {
    const d = event.data
    const remote = d.class === 'remote'
      ? create(RemoteRepositorySettingsSchema, {
          url: d.url ?? '', username: d.username ?? '', password: d.password ?? '', indexUrl: d.indexUrl ?? '',
          offline: d.offline, disableCache: d.disableCache,
          cachePeriodSeconds: BigInt(d.cachePeriodSeconds), timeoutMilliseconds: BigInt(d.timeoutMilliseconds),
          missedRetrievalCachePeriodSeconds: BigInt(d.missedRetrievalSeconds),
          cooldownDays: d.cooldownDays,
          quarantine: d.quarantine
        })
      : undefined
    const virtual = d.class === 'virtual' ? create(VirtualRepositorySettingsSchema, { members: d.repositories, deployTo: d.defaultDeploymentRepo ?? '', localOnlyPatterns: d.localOnlyPatterns }) : undefined
    const maxUploadSize = d.class === 'local' ? sizeInBytes(d.maxUploadSizeMiB, MiB, r?.maxUploadSize) : 0n
    const maxStorage = d.class === 'local' ? sizeInBytes(d.maxStorageGiB, GiB, r?.maxStorage) : 0n
    const immutable = d.class === 'local' && d.immutable
    const indexes = {
      signingKey: defaultSigningKey[d.format] && d.class !== 'remote' ? d.signingKey ?? '' : '',
      metadataDepth: d.format === 'rpm' && d.class === 'local' ? d.metadataDepth : 0,
      architectures: d.format === 'debian' && d.class === 'virtual' ? d.architectures : []
    }
    if (r) {
      await relic.repositories.updateRepository({ repository: { ...r, description: d.description ?? '', remote, virtual, maxUploadSize, maxStorage, immutable, ...indexes } })
    } else {
      await relic.repositories.createRepository({
        repository: { name: d.name, class: repositoryClassOf(d.class), format: repositoryFormatOf(d.format), description: d.description ?? '', remote, virtual, maxUploadSize, maxStorage, immutable, ...indexes }
      })
    }
    toast.add({ title: `Repository ${d.name} ${r ? 'saved' : 'created'}`, color: 'success', icon: 'i-lucide-check' })
    emit('close', true)
  } catch (err) {
    toast.add({ title: `Could not ${r ? 'save' : 'create'} the repository`, description: relicErrorMessage(err, ''), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <USlideover :title="r ? `Edit ${r.name}` : 'New repository'" :close="{ onClick: () => emit('close', false) }">
    <template #body>
      <UForm
        id="repository-form"
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField name="class" label="Class" required>
          <URadioGroup
            v-model="state.class"
            :disabled="!!r"
            :items="[
              { label: 'Local', value: 'local', description: 'Artifacts deployed here' },
              { label: 'Remote', value: 'remote', description: 'A cache of an upstream repository' },
              { label: 'Virtual', value: 'virtual', description: 'One URL over several repositories' }
            ]"
          >
            <template #label="{ item }">
              <span class="inline-flex items-center gap-1.5">
                <UIcon :name="repositoryClassIcon(item.value)" class="size-4 shrink-0" :class="repositoryClassColor(item.value)" />
                {{ item.label }}
              </span>
            </template>
          </URadioGroup>
        </UFormField>
        <UFormField name="format" label="Format" required>
          <USelect
            v-model="state.format"
            :disabled="!!r"
            :items="repositoryFormats.map(f => ({ label: f, value: f, icon: formatIcon(f) }))"
            :icon="formatIcon(state.format)"
            class="w-full"
          />
        </UFormField>
        <UFormField name="name" label="Name" required>
          <UInput
            v-model="state.name"
            :disabled="!!r"
            placeholder="libs-release"
            class="w-full"
          />
        </UFormField>
        <UFormField name="description" label="Description">
          <UInput v-model="state.description" class="w-full" />
        </UFormField>
        <UFormField
          v-if="state.class === 'local'"
          name="maxUploadSizeMiB"
          label="Maximum upload size (MiB)"
          description="0 leaves only the server's limit."
        >
          <UInputNumber v-model="state.maxUploadSizeMiB" :min="0" class="w-full" />
        </UFormField>
        <UFormField
          v-if="state.class === 'local'"
          name="maxStorageGiB"
          label="Storage quota (GiB)"
          :description="r?.usedBytes ? `0 is no quota. Its files take ${formatBytes(Number(r.usedBytes))} now.` : '0 is no quota.'"
        >
          <UInputNumber v-model="state.maxStorageGiB" :min="0" class="w-full" />
        </UFormField>
        <UFormField
          v-if="signsIndexes"
          name="signingKey"
          label="Signing key"
          :description="`The key pair that signs the indexes; Relic creates it the first time it signs. Empty uses ${defaultSigningKey[state.format ?? '']}.`"
          hint="Optional"
        >
          <UInput v-model="state.signingKey" :placeholder="defaultSigningKey[state.format ?? '']" class="w-full" />
        </UFormField>
        <UFormField
          v-if="state.format === 'rpm' && state.class === 'local'"
          name="metadataDepth"
          label="Metadata depth"
          description="0 indexes the whole repository at repodata/; 1 indexes each top folder (el9/repodata/, el10/repodata/)."
        >
          <UInputNumber
            v-model="state.metadataDepth"
            :min="0"
            :max="16"
            class="w-full"
          />
        </UFormField>
        <UFormField v-if="state.class === 'local'" name="immutable">
          <USwitch v-model="state.immutable" label="Immutable" description="Refuse replacing a file, or moving a container tag, to other content." />
        </UFormField>
        <UFormField
          v-if="state.class === 'remote'"
          name="url"
          label="Upstream URL"
          required
        >
          <UInput v-model="state.url" class="w-full" />
        </UFormField>
        <div v-if="state.class === 'remote'" class="grid grid-cols-2 items-end gap-4">
          <UFormField name="username" label="Upstream username" hint="Optional">
            <UInput v-model="state.username" autocomplete="off" class="w-full" />
          </UFormField>
          <UFormField name="password" label="Upstream password or token" :hint="r ? 'Leave empty to keep' : 'Optional'">
            <UInput
              v-model="state.password"
              type="password"
              autocomplete="new-password"
              class="w-full"
            />
          </UFormField>
        </div>
        <template v-if="state.class === 'remote'">
          <UFormField
            name="indexUrl"
            label="Package index URL"
            description="When the index is not at the upstream URL (the PyPI registry, a NuGet v3 feed)."
            hint="Optional"
          >
            <UInput v-model="state.indexUrl" class="w-full" />
          </UFormField>
          <div class="grid grid-cols-2 items-end gap-4">
            <UFormField name="cachePeriodSeconds" label="Metadata cache (seconds)" description="0 uses the default.">
              <UInputNumber v-model="state.cachePeriodSeconds" :min="0" class="w-full" />
            </UFormField>
            <UFormField name="timeoutMilliseconds" label="Upstream timeout (ms)" description="0 uses the default.">
              <UInputNumber
                v-model="state.timeoutMilliseconds"
                :min="0"
                :step="1000"
                class="w-full"
              />
            </UFormField>
          </div>
          <UFormField
            name="missedRetrievalSeconds"
            label="Remember upstream misses (seconds)"
            description="How long a file the upstream does not have is not asked for again; 0 asks every time."
          >
            <UInputNumber
              v-model="state.missedRetrievalSeconds"
              :min="0"
              :step="60"
              class="w-full"
            />
          </UFormField>
          <UFormField
            name="cooldownDays"
            label="Cooldown (days)"
            description="Hide and refuse npm and PyPI versions published upstream more recently than this; 0 turns it off."
          >
            <UInputNumber
              v-model="state.cooldownDays"
              :min="0"
              :max="365"
              class="w-full"
            />
          </UFormField>
          <UFormField name="quarantine">
            <USwitch
              v-model="state.quarantine"
              label="Quarantine"
              description="Hold what the repository caches until Relic Scan has scanned it and found it not malicious."
            />
          </UFormField>
          <UFormField name="offline">
            <USwitch v-model="state.offline" label="Offline" description="Serve only what is cached; never contact the upstream." />
          </UFormField>
          <UFormField name="disableCache">
            <USwitch v-model="state.disableCache" label="Do not cache" description="Serve upstream files without keeping them." />
          </UFormField>
        </template>
        <template v-if="state.class === 'virtual'">
          <UFormField name="repositories" label="Members, in resolution order" required>
            <USelectMenu
              v-model="state.repositories"
              :items="members"
              multiple
              class="w-full"
            />
          </UFormField>
          <UFormField name="defaultDeploymentRepo" label="Default deployment repository" hint="Optional">
            <USelect v-model="state.defaultDeploymentRepo" :items="locals" class="w-full" />
          </UFormField>
          <UFormField
            v-if="state.format === 'debian'"
            name="architectures"
            label="Architectures"
            description="The architectures taken from remote members' indexes; empty is amd64 and arm64."
            hint="Optional"
          >
            <UInputTags v-model="state.architectures" placeholder="Add an architecture" class="w-full" />
          </UFormField>
          <UFormField
            name="localOnlyPatterns"
            label="Local-only names"
            description="Package names or paths that resolve only from local members, never from remote ones: @acme/*, com/acme, acme-*."
            hint="Optional"
          >
            <UInputTags v-model="state.localOnlyPatterns" placeholder="Add a pattern" class="w-full" />
          </UFormField>
        </template>
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
        form="repository-form"
        :label="r ? 'Save' : 'Create'"
        :loading="saving"
      />
    </template>
  </USlideover>
</template>
