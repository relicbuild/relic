<script setup lang="ts">
import type { ComponentView, FindingView } from '~/utils/view-models'

const props = defineProps<{
  finding: FindingView
  locations?: ComponentView['locations']
}>()
const emit = defineEmits<{ close: [] }>()

const paragraphs = computed(() =>
  props.finding.summary.split(/\n\s*\n/).map(p => p.replace(/\s*\n\s*/g, ' ').trim()).filter(Boolean)
)
</script>

<template>
  <USlideover :title="finding.id" :close="{ onClick: () => emit('close') }">
    <template #description>
      <UBadge :color="severityColor(finding.severity)" variant="subtle" :label="severityLabel(finding.severity)" />
    </template>
    <template #body>
      <div class="space-y-6">
        <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-3 text-sm">
          <dt class="text-muted">
            Package
          </dt>
          <dd class="text-default">
            {{ finding.package }} <span class="text-muted">{{ finding.version }}</span>
          </dd>
          <dt class="text-muted">
            Package URL
          </dt>
          <dd class="font-mono text-xs break-all text-default">
            {{ finding.purl }}
          </dd>
          <dt class="text-muted">
            Fixed in
          </dt>
          <dd class="text-default">
            {{ finding.fixedVersions.join(', ') || 'No fix available' }}
          </dd>
          <template v-if="finding.reachability !== 'unknown'">
            <dt class="text-muted">
              Reachability
            </dt>
            <dd class="space-y-1">
              <UBadge
                :color="reachabilityBadges[finding.reachability].color"
                variant="subtle"
                :icon="reachabilityBadges[finding.reachability].icon"
                :label="reachabilityBadges[finding.reachability].label"
              />
              <p v-if="finding.reachabilityTrace" class="font-mono text-xs break-all text-muted">
                {{ finding.reachabilityTrace }}
              </p>
              <p v-else class="text-xs text-muted">
                govulncheck found no call path from the executable's code to the vulnerable functions.
              </p>
            </dd>
          </template>
          <template v-if="finding.cves.length">
            <dt class="text-muted">
              CVEs
            </dt>
            <dd class="text-default">
              {{ finding.cves.join(', ') }}
            </dd>
          </template>
        </dl>

        <div v-if="locations?.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Found in
          </h3>
          <ul class="space-y-1">
            <li v-for="l in locations" :key="l.path + l.layerId" class="text-sm">
              <span class="font-mono text-xs break-all text-default">{{ l.path }}</span>
              <div v-if="l.layerId" class="font-mono text-xs break-all text-muted">
                layer {{ l.layerId }}
              </div>
            </li>
          </ul>
        </div>

        <div v-if="finding.cvss.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            CVSS
          </h3>
          <div v-for="c in finding.cvss" :key="c.version + c.vector" class="flex items-center gap-3 text-sm">
            <UBadge color="neutral" variant="outline" :label="`v${c.version} ${c.score.toFixed(1)}`" />
            <span class="font-mono text-xs break-all text-muted">{{ c.vector }}</span>
          </div>
        </div>

        <div class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            Description
          </h3>
          <p v-for="(para, i) in paragraphs" :key="i" class="text-sm text-default">
            {{ para }}
          </p>
          <p v-if="!paragraphs.length" class="text-sm text-muted">
            No description.
          </p>
        </div>

        <div v-if="finding.urls.length" class="space-y-2">
          <h3 class="text-sm font-medium text-highlighted">
            References
          </h3>
          <ul class="space-y-1 text-sm">
            <li v-for="u in finding.urls" :key="u" class="break-all">
              <ULink :to="u" target="_blank" external>
                {{ u }}
              </ULink>
            </li>
          </ul>
        </div>
      </div>
    </template>
  </USlideover>
</template>
