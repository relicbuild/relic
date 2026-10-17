import type { GetClientEndpointsResponse, Repository } from '~/gen/relic/v1/repository_pb'

export interface ClientSetupSnippet {
  title: string
  code: string
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
    case 'rpm':
      out.push({ title: 'dnf', code: `# /etc/yum.repos.d/relic-${repo.name}.repo\n[relic-${repo.name}]\nname=Relic ${repo.name}\nbaseurl=${pm}\nusername=${user}\npassword=${token}\ngpgcheck=0` })
      break
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
