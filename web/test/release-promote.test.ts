import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountSuspended } from '@nuxt/test-utils/runtime'
import { create } from '@bufbuild/protobuf'
import { ListRepositoriesResponseSchema } from '~/gen/relic/v1/repository_pb'
import PromoteModal from '~/components/release/PromoteModal.vue'

describe('release promote dialog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    clearNuxtData('release:environments')
  })

  it('says each artifact goes into one repository of the environment', async () => {
    vi.spyOn(useRelic().repositories, 'listRepositories').mockResolvedValue(create(ListRepositoriesResponseSchema, {}))

    const modal = await mountSuspended(PromoteModal, {
      props: { project: '', name: 'app', version: '1.0' },
      attrs: { open: true }
    })

    const text = document.body.textContent ?? ''
    expect(text).toContain('Promote app/1.0')
    expect(text).toContain('one local repository')
    expect(text).not.toContain('every repository')

    modal.unmount()
  }, 60000)
})
