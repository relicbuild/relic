import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { Format, GetClientEndpointsResponseSchema, RepositoryClass, RepositorySchema } from '~/gen/relic/v1/repository_pb'
import { clientSetupSnippets, tokenExpiry } from '~/utils/client-setup'

const endpoints = create(GetClientEndpointsResponseSchema, {
  files: 'http://relic.test/api/v1/files/r/',
  packageManager: 'http://relic.test/api/pypi/r/simple',
  containerRegistry: 'relic.test/r'
})
const repo = (format: Format) => create(RepositorySchema, { name: 'r', class: RepositoryClass.LOCAL, format })
const code = (format: Format, token: string, title: string, user = 'alice') =>
  clientSetupSnippets(repo(format), endpoints, user, token).find(s => s.title === title)?.code

describe('clientSetupSnippets', () => {
  it.each([
    { name: 'pip keeps the placeholder readable', format: Format.PYPI, title: 'pip.conf', token: '<token>', want: 'http://alice:<token>@relic.test/' },
    { name: 'pip percent-encodes a real token', format: Format.PYPI, title: 'pip.conf', token: 'a/b@c', want: 'http://alice:a%2Fb%40c@relic.test/' },
    { name: 'GOPROXY keeps the placeholder readable', format: Format.GOMOD, title: 'GOPROXY', token: '<token>', want: 'alice:<token>@' },
    { name: 'dnf points a repository file at the repository', format: Format.RPM, title: 'dnf', token: '<token>', want: 'baseurl=http://relic.test/api/pypi/r/simple\nusername=alice\npassword=<token>' },
    { name: 'docker logs in to the registry host', format: Format.DOCKER, title: 'docker', token: '<token>', want: 'docker login relic.test -u alice' },
    { name: 'every format gets curl with the file URL', format: Format.GENERIC, title: 'curl', token: '<token>', want: 'http://relic.test/api/v1/files/r/<path>/<file>' },
    { name: 'docker quotes a user name with a space', format: Format.DOCKER, title: 'docker', token: '<token>', user: 'john doe', want: 'docker login relic.test -u \'john doe\' -p <token>' },
    { name: 'curl quotes a user name with a space', format: Format.GENERIC, title: 'curl', token: '<token>', user: 'john doe', want: 'curl -u \'john doe\':<token> -T' }
  ])('$name', ({ format, title, token, user, want }) => {
    expect(code(format, token, title, user)).toContain(want)
  })
})

describe('tokenExpiry', () => {
  it('lets an administrator mint a token that never expires', () => {
    expect(tokenExpiry(true)).toEqual({ minDays: 0, maxDays: 3650, label: 'Expires after (days, 0 = never)' })
  })

  it('bounds the token of a user who is not an administrator to 365 days', () => {
    expect(tokenExpiry(false)).toEqual({ minDays: 1, maxDays: 365, label: 'Expires after (days, at most 365)' })
  })
})
