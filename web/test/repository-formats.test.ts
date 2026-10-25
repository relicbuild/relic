import { describe, expect, it } from 'vitest'
import { Format } from '~/gen/relic/v1/repository_pb'
import { upstreamURL } from '~/utils/repository-formats'

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

describe('upstreamURL', () => {
  it.each([
    { name: 'fills an empty URL', format: 'npm', current: '', want: 'https://registry.npmjs.org' },
    { name: 'replaces the default of another format', format: 'maven', current: 'https://registry.npmjs.org', want: 'https://repo1.maven.org/maven2' },
    { name: 'clears a default for a format without one', format: 'generic', current: 'https://files.pythonhosted.org', want: '' },
    { name: 'keeps a URL the user typed', format: 'maven', current: 'https://mirror.example.com/maven', want: 'https://mirror.example.com/maven' }
  ])('$name', ({ format, current, want }) => {
    expect(upstreamURL(format, current)).toBe(want)
  })
})
