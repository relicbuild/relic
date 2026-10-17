import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { GetContainerImageResponseSchema } from '~/gen/relic/v1/package_pb'
import { imageInfo } from '~/utils/mappers'

describe('imageInfo', () => {
  it.each([
    { name: 'os and architecture', os: 'linux', architecture: 'amd64', want: 'linux/amd64' },
    { name: 'os without architecture', os: 'linux', architecture: '', want: 'linux' },
    { name: 'neither', os: '', architecture: '', want: undefined }
  ])('names the platform from $name', ({ os, architecture, want }) => {
    expect(imageInfo(create(GetContainerImageResponseSchema, { os, architecture })).platform).toBe(want)
  })
})
