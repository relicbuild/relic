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
    { name: 'Go leaves private modules out of the checksum database', format: Format.GOMOD, title: 'GOPROXY', token: '<token>', want: 'export GONOSUMDB=' },
    { name: 'cargo names the sparse index', format: Format.CARGO, title: '~/.cargo/config.toml', token: '<token>', want: 'index = "sparse+http://relic.test/api/pypi/r/simple"' },
    { name: 'cargo sends the token as a bearer token', format: Format.CARGO, title: '~/.cargo/credentials.toml', token: '<token>', want: 'token = "Bearer <token>"' },
    { name: 'conan adds the remote and logs in', format: Format.CONAN, title: 'conan', token: '<token>', want: 'CONAN_PASSWORD=<token> conan remote login r alice' },
    { name: 'bundler mirrors rubygems.org', format: Format.RUBYGEMS, title: 'bundler', token: '<token>', want: 'mirror.https://rubygems.org http://relic.test/' },
    { name: 'gem pushes with the token as the key', format: Format.RUBYGEMS, title: 'gem', token: '<token>', want: 'GEM_HOST_API_KEY=<token> gem push' },
    { name: 'terraform gets credentials for the host', format: Format.TERRAFORM, title: '~/.terraformrc', token: '<token>', want: 'credentials "relic.test"' },
    { name: 'terraform names modules by repository', format: Format.TERRAFORM, title: 'module source', token: '<token>', want: 'relic.test/r__<namespace>/<name>/<provider>' },
    { name: 'huggingface_hub gets the endpoint', format: Format.HUGGINGFACE, title: 'huggingface_hub', token: '<token>', want: 'export HF_ENDPOINT=http://relic.test/' },
    { name: 'apk gets a branch of the repository', format: Format.ALPINE, title: '/etc/apk/repositories', token: '<token>', want: 'http://alice:<token>@relic.test/api/pypi/r/simple/<branch>/main' },
    { name: 'apt gets a source', format: Format.DEBIAN, title: '/etc/apt/sources.list.d/relic-r.list', token: '<token>', want: 'deb http://relic.test/' },
    { name: 'nix gets a substituter', format: Format.NIX, title: '~/.config/nix/nix.conf', token: '<token>', want: 'extra-substituters = http://relic.test/' },
    { name: 'git-lfs gets the LFS URL', format: Format.GIT_LFS, title: '.lfsconfig', token: '<token>', want: 'url = http://relic.test/' },
    { name: 'docker logs in to the registry host', format: Format.DOCKER, title: 'docker', token: '<token>', want: 'docker login relic.test -u alice' },
    { name: 'every format gets curl with the file URL', format: Format.GENERIC, title: 'curl', token: '<token>', want: 'http://relic.test/api/v1/files/r/<path>/<file>' },
    { name: 'docker quotes a user name with a space', format: Format.DOCKER, title: 'docker', token: '<token>', user: 'john doe', want: 'docker login relic.test -u \'john doe\' -p <token>' },
    { name: 'curl quotes a user name with a space', format: Format.GENERIC, title: 'curl', token: '<token>', user: 'john doe', want: 'curl -u \'john doe\':<token> -T' }
  ])('$name', ({ format, title, token, user, want }) => {
    expect(code(format, token, title, user)).toContain(want)
  })
})

describe('clientSetupSnippets for dnf', () => {
  const dnf = (settings: { class: RepositoryClass, metadataDepth?: number }) =>
    clientSetupSnippets(create(RepositorySchema, { name: 'r', format: Format.RPM, ...settings }), endpoints, 'alice', '<token>')
      .find(s => s.title === 'dnf')?.code ?? ''

  it('checks the signature of a local repository with its key', () => {
    const code = dnf({ class: RepositoryClass.LOCAL })
    expect(code).toContain('curl -u alice:<token> -o /etc/pki/rpm-gpg/RPM-GPG-KEY-relic-r http://relic.test/api/pypi/r/simple/repodata/repomd.xml.key')
    expect(code).toContain('repo_gpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-relic-r')
  })

  it('leaves the signature of a remote to its upstream', () => {
    const code = dnf({ class: RepositoryClass.REMOTE })
    expect(code).toContain('repo_gpgcheck=0')
    expect(code).not.toContain('gpgkey=')
  })

  it('points a repository with a metadata depth at a folder', () => {
    expect(dnf({ class: RepositoryClass.LOCAL, metadataDepth: 1 })).toContain('baseurl=http://relic.test/api/pypi/r/simple/<folder>\n')
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
