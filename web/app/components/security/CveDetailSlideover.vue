<script setup lang="ts">
import { cveDetailView } from '~/utils/mappers'

const props = defineProps<{ id: string }>()
const emit = defineEmits<{ close: [] }>()
const { scans } = useRelic()

// Not awaited: overlays are mounted without a Suspense boundary.
const { data: cve, status, error, refresh } = useAsyncData(
  () => `security:cve:${props.id}`,
  async () => cveDetailView((await scans.getCve({ id: props.id })).cve!),
  { watch: [() => props.id] }
)

const day = (iso?: string) => formatDay(iso) || '—'
</script>

<template>
  <USlideover :title="id" :close="{ onClick: () => emit('close') }" :ui="{ content: 'max-w-2xl' }">
    <template #description>
      <div v-if="cve" class="flex flex-wrap items-center gap-2">
        <UBadge :color="severityColor(cve.severity)" variant="subtle" :label="severityLabel(cve.severity)" />
        <UBadge
          v-if="cve.kev"
          color="error"
          variant="solid"
          icon="i-lucide-siren"
          label="Known exploited"
        />
        <UBadge
          v-if="cve.ransomware"
          color="error"
          variant="outline"
          label="Ransomware use"
        />
        <span class="text-sm text-muted">{{ cve.title }}</span>
      </div>
    </template>
    <template #body>
      <LoadError :error="error" :what="id" @retry="refresh()" />
      <div v-if="status === 'pending' && !cve" class="space-y-3">
        <USkeleton class="h-4 w-2/3" />
        <USkeleton class="h-24 w-full" />
      </div>
      <div v-else-if="cve" class="space-y-6">
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <UCard :ui="{ body: 'p-3 sm:p-3' }">
            <div class="text-xs text-muted">
              CVSS {{ cve.cvss.version ? `v${cve.cvss.version}` : '' }}
            </div>
            <div class="text-xl font-semibold text-highlighted tabular-nums">
              {{ cve.cvss.score?.toFixed(1) ?? '—' }}
            </div>
            <div v-if="cve.cvss.source" class="text-xs text-muted">
              by {{ cve.cvss.source }}
            </div>
          </UCard>
          <UCard :ui="{ body: 'p-3 sm:p-3' }">
            <div class="text-xs text-muted">
              EPSS (30-day exploit probability)
            </div>
            <div class="text-xl font-semibold tabular-nums" :class="cve.epss !== undefined && cve.epss >= 0.1 ? 'text-error' : 'text-highlighted'">
              {{ formatPercent(cve.epss) }}
            </div>
            <div v-if="cve.epssPercentile !== undefined" class="text-xs text-muted">
              percentile {{ (cve.epssPercentile * 100).toFixed(1) }} · {{ day(cve.epssDate) }}
            </div>
          </UCard>
          <UCard :ui="{ body: 'p-3 sm:p-3' }">
            <div class="text-xs text-muted">
              CISA SSVC
            </div>
            <div class="text-sm text-highlighted">
              Exploitation: {{ cve.exploitation || '—' }}
            </div>
            <div class="text-xs text-muted">
              Automatable: {{ cve.automatable || '—' }} · Impact: {{ cve.technicalImpact || '—' }}
            </div>
          </UCard>
        </div>

        <UAlert
          v-if="cve.kevEntry"
          color="error"
          variant="subtle"
          icon="i-lucide-siren"
          title="In CISA's Known Exploited Vulnerabilities catalog"
          :description="`Added ${day(cve.kevEntry.added)}${cve.kevEntry.due ? `, federal remediation due ${day(cve.kevEntry.due)}` : ''}. ${cve.kevEntry.action ?? ''}`"
        />

        <div v-if="cve.description" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Description
          </h3>
          <p class="text-sm text-default whitespace-pre-line">
            {{ cve.description }}
          </p>
        </div>

        <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <dt class="text-muted">
            Published
          </dt>
          <dd>{{ day(cve.published) }}</dd>
          <template v-if="cve.cvss.vector">
            <dt class="text-muted">
              Vector
            </dt>
            <dd class="font-mono text-xs break-all">
              {{ cve.cvss.vector }}
            </dd>
          </template>
          <template v-if="cve.cwes.length">
            <dt class="text-muted">
              Weakness
            </dt>
            <dd class="flex flex-wrap gap-1">
              <UBadge
                v-for="c in cve.cwes"
                :key="c"
                color="neutral"
                variant="outline"
                :label="c"
              />
            </dd>
          </template>
          <template v-if="!cve.recordChecked">
            <dt class="text-muted">
              Record
            </dt>
            <dd class="text-muted">
              Not read yet; the worker fetches CVE records hourly.
            </dd>
          </template>
        </dl>

        <div class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Affected artifacts ({{ cve.findings.length }})
          </h3>
          <ul class="divide-y divide-default rounded-md border border-default">
            <li v-for="(f, i) in cve.findings" :key="i" class="p-3 text-sm">
              <ULink :to="{ path: `/repositories/${f.repo}`, query: { path: f.path } }" class="break-all font-medium">
                {{ f.repo }}/{{ f.path }}
              </ULink>
              <div class="text-xs text-muted">
                {{ f.package }} {{ f.version }}
                <template v-if="f.fixedVersions.length">
                  · fixed in {{ f.fixedVersions.join(', ') }}
                </template>
                <template v-if="f.finding !== cve.id">
                  · via {{ f.finding }}
                </template>
              </div>
            </li>
          </ul>
        </div>

        <div v-if="cve.references.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            References
          </h3>
          <ul class="space-y-1">
            <li v-for="r in cve.references" :key="r" class="text-xs break-all">
              <ULink :to="r" target="_blank" rel="noopener noreferrer">
                {{ r }}
              </ULink>
            </li>
          </ul>
        </div>
      </div>
    </template>
  </USlideover>
</template>
