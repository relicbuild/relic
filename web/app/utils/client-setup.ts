import { RepositoryClass } from '~/gen/relic/v1/repository_pb'
import type { GetClientEndpointsResponse, Repository } from '~/gen/relic/v1/repository_pb'

export interface ClientSetupSnippet {
  title: string
  code: string
}

export interface TokenExpiry {
  minDays: number
  maxDays: number
  label: string
}

// tokenExpiry is the lifetime a user may give a token they mint: the server
// keeps the token of a user who is not an administrator 365 days at most.
export function tokenExpiry(admin: boolean): TokenExpiry {
  return admin
    ? { minDays: 0, maxDays: 3650, label: 'Expires after (days, 0 = never)' }
    : { minDays: 1, maxDays: 365, label: 'Expires after (days, at most 365)' }
}

function withCredentials(url: string, user: string, token: string): string {
  return url.replace('://', `://${userinfo(user)}:${userinfo(token)}@`)
}

function userinfo(value: string): string {
  return /^<\w+>$/.test(value) ? value : encodeURIComponent(value)
}

// clientSetupSnippets returns package-manager configuration for a repository
// from the endpoints the server reports for it.
export function clientSetupSnippets(repo: Repository, endpoints: GetClientEndpointsResponse, user: string, token: string): ClientSetupSnippet[] {
  const pm = endpoints.packageManager
  const cr = endpoints.containerRegistry
  const registryHost = cr.split('/')[0] ?? ''
  const out: ClientSetupSnippet[] = []
  switch (formatName(repo.format)) {
    case 'npm':
      out.push({ title: '.npmrc', code: `registry=${pm}\n${pm.replace(/^https?:/, '')}:_authToken=${token}` })
      break
    case 'maven':
    case 'gradle':
      out.push({ title: 'settings.xml', code: `<settings>\n  <servers>\n    <server><id>relic</id><username>${user}</username><password>${token}</password></server>\n  </servers>\n  <mirrors>\n    <mirror><id>relic</id><mirrorOf>*</mirrorOf><url>${pm}</url></mirror>\n  </mirrors>\n</settings>` })
      break
    case 'pypi':
      out.push({ title: 'pip.conf', code: `[global]\nindex-url = ${withCredentials(pm, user, token)}` })
      break
    case 'gomod':
      out.push({ title: 'GOPROXY', code: `export GOPROXY=${withCredentials(pm, user, token)}` })
      break
    case 'nuget':
      out.push({ title: 'dotnet', code: `dotnet nuget add source ${pm} --name relic --username ${shellWord(user)} --password ${token} --store-password-in-clear-text` })
      break
    case 'docker':
    case 'oci':
      out.push({ title: 'docker', code: `docker login ${registryHost} -u ${shellWord(user)} -p ${token}\ndocker pull ${cr}/<image>:<tag>\ndocker push ${cr}/<image>:<tag>` })
      break
    case 'helm':
      out.push({ title: 'helm', code: `helm repo add ${repo.name} ${pm} --username ${shellWord(user)} --password ${token}` })
      break
    case 'rpm': {
      const baseURL = repo.metadataDepth > 0 ? `${pm.replace(/\/$/, '')}/<folder>` : pm
      const keyFile = `/etc/pki/rpm-gpg/RPM-GPG-KEY-relic-${repo.name}`
      const remote = repo.class === RepositoryClass.REMOTE
      const fetchKey = remote ? '' : `curl -u ${shellWord(user)}:${token} -o ${keyFile} ${baseURL.replace(/\/$/, '')}/repodata/repomd.xml.key\n`
      const signature = remote ? 'repo_gpgcheck=0' : `repo_gpgcheck=1\ngpgkey=file://${keyFile}`
      out.push({ title: 'dnf', code: `${fetchKey}# /etc/yum.repos.d/relic-${repo.name}.repo\n[relic-${repo.name}]\nname=Relic ${repo.name}\nbaseurl=${baseURL}\nusername=${user}\npassword=${token}\ngpgcheck=0\n${signature}` })
      break
    }
    case 'helmoci':
      out.push({ title: 'helm', code: `helm registry login ${registryHost} -u ${shellWord(user)} -p ${token}\nhelm push <chart>.tgz oci://${cr}` })
      break
  }
  out.push({
    title: 'curl',
    code: `curl -u ${shellWord(user)}:${token} -T <file> ${endpoints.files}<path>/<file>\ncurl -u ${shellWord(user)}:${token} -O ${endpoints.files}<path>/<file>`
  })
  return out
}
