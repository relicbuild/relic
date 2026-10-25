import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mountSuspended } from '@nuxt/test-utils/runtime'
import { create } from '@bufbuild/protobuf'
import { GetBuildRunResponseSchema } from '~/gen/relic/v1/build_pb'
import { ListEvidenceResponseSchema } from '~/gen/relic/v1/evidence_pb'
import { GetReleaseResponseSchema, ListReleasePromotionsResponseSchema } from '~/gen/relic/v1/release_pb'
import { PrincipalSchema } from '~/gen/relic/v1/session_pb'
import BuildRun from '~/pages/builds/[name]/[number].vue'
import ReleaseVersion from '~/pages/releases/[name]/[version].vue'

describe('detail page tabs', () => {
  beforeEach(() => {
    useSession().me.value = create(PrincipalSchema, { username: 'dev' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    clearNuxtData()
    useSession().me.value = null
  })

  it.each([
    {
      name: 'release',
      mount: async () => {
        const relic = useRelic()
        vi.spyOn(relic.releases, 'getRelease').mockResolvedValue(create(GetReleaseResponseSchema, {
          release: { name: 'rel1', version: '1.0' }
        }))
        vi.spyOn(relic.releases, 'listReleasePromotions').mockResolvedValue(create(ListReleasePromotionsResponseSchema))
        vi.spyOn(relic.evidence, 'listEvidence').mockResolvedValue(create(ListEvidenceResponseSchema))
        return mountSuspended(ReleaseVersion, { route: '/releases/rel1/1.0' })
      },
      labels: ['Artifacts', 'Promotions', 'Gate decisions', 'Evidence']
    },
    {
      name: 'build',
      mount: async () => {
        vi.spyOn(useRelic().builds, 'getBuildRun').mockResolvedValue(create(GetBuildRunResponseSchema, {
          name: 'app',
          number: '7'
        }))
        return mountSuspended(BuildRun, { route: '/builds/app/7' })
      },
      labels: ['Artifacts', 'Dependencies', 'Details']
    }
  ])('scrolls the $name tabs instead of truncating their labels', async ({ mount, labels }) => {
    const page = await mount()

    const list = page.find('[data-slot="list"]')
    expect(list.classes()).toContain('overflow-x-auto')
    const triggers = list.findAll('[data-slot="trigger"]')
    expect(triggers.map(trigger => trigger.find('[data-slot="label"]').text())).toEqual(labels)
    for (const trigger of triggers) {
      expect(trigger.classes()).toContain('shrink-0')
    }
  })
})
