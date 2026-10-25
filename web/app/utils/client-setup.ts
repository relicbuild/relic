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

function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return '<host>'
  }
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
      out.push({ title: 'GOPROXY', code: `export GOPROXY=${withCredentials(pm, user, token)}\n# modules published to Relic only, not in the public checksum database\nexport GONOSUMDB=<module path patterns>` })
      break
    case 'cargo':
      out.push({ title: '~/.cargo/config.toml', code: `[registries.${repo.name}]\nindex = "sparse+${pm}"\ncredential-provider = ["cargo:token"]` })
      out.push({ title: '~/.cargo/credentials.toml', code: `[registries.${repo.name}]\ntoken = "Bearer ${token}"` })
      break
    case 'conan':
      out.push({ title: 'conan', code: `conan remote add --force ${repo.name} ${pm}\nCONAN_PASSWORD=${token} conan remote login ${repo.name} ${shellWord(user)}` })
      break
    case 'rubygems':
      out.push({ title: 'bundler', code: `bundle config set --global mirror.https://rubygems.org ${pm}\nbundle config set --global ${pm} ${shellWord(user)}:${token}` })
      out.push({ title: 'gem', code: `gem sources --add ${withCredentials(pm, user, token)}\nGEM_HOST_API_KEY=${token} gem push <gem> --host ${pm.replace(/\/$/, '')}` })
      break
    case 'terraform':
      out.push({ title: '~/.terraformrc', code: `credentials "${hostOf(pm)}" {\n  token = "${token}"\n}\nprovider_installation {\n  network_mirror {\n    url = "${pm}"\n  }\n}` })
      out.push({ title: 'module source', code: `source = "${hostOf(pm)}/${repo.name}__<namespace>/<name>/<provider>"` })
      break
    case 'huggingface':
      out.push({ title: 'huggingface_hub', code: `export HF_ENDPOINT=${pm}\nexport HF_TOKEN=${token}` })
      break
    case 'alpine':
      out.push({ title: '/etc/apk/repositories', code: `${withCredentials(pm, user, token).replace(/\/$/, '')}/<branch>/main` })
      break
    case 'debian':
      out.push({ title: `/etc/apt/sources.list.d/relic-${repo.name}.list`, code: `deb ${pm} <distribution> main` })
      out.push({ title: `/etc/apt/auth.conf.d/relic-${repo.name}.conf`, code: `machine ${pm} login ${user} password ${token}` })
      break
    case 'nix':
      out.push({ title: '~/.config/nix/nix.conf', code: `extra-substituters = ${pm}\nnetrc-file = /home/<you>/.netrc` })
      out.push({ title: '~/.netrc', code: `machine ${hostOf(pm)} login ${user} password ${token}` })
      break
    case 'gitlfs':
      out.push({ title: '.lfsconfig', code: `[lfs]\n\turl = ${pm}` })
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
