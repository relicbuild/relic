import { describe, expect, it } from 'vitest'
import { Format } from '~/gen/relic/v1/repository_pb'

describe('repositoryFormats', () => {
  it('names a format the API knows for each entry', () => {
    for (const name of repositoryFormats) {
      expect(repositoryFormatOf(name), name).not.toBe(Format.UNSPECIFIED)
    }
  })

  it('offers every package format with generated indexes', () => {
    expect(repositoryFormats).toEqual(expect.arrayContaining(['alpine', 'debian', 'rpm', 'nix']))
  })
})
