export const repositoryFormats = [
  'generic', 'maven', 'gradle', 'npm', 'pypi', 'gomod', 'docker', 'oci', 'helm', 'helmoci', 'nuget', 'rubygems',
  'cargo', 'conan', 'terraform', 'huggingface', 'alpine', 'debian', 'rpm', 'nix', 'mcp', 'skills'
]

const defaultUpstreams: Record<string, string> = {
  maven: 'https://repo1.maven.org/maven2',
  gradle: 'https://repo1.maven.org/maven2',
  npm: 'https://registry.npmjs.org',
  pypi: 'https://files.pythonhosted.org',
  gomod: 'https://proxy.golang.org',
  docker: 'https://registry-1.docker.io',
  nuget: 'https://www.nuget.org',
  rubygems: 'https://rubygems.org',
  cargo: 'https://index.crates.io',
  conan: 'https://center2.conan.io',
  huggingface: 'https://huggingface.co',
  alpine: 'https://dl-cdn.alpinelinux.org/alpine',
  debian: 'https://deb.debian.org/debian'
}
const defaultUpstreamURLs = new Set(Object.values(defaultUpstreams))

// upstreamURL returns a remote's URL for a format: the format's public registry, unless the user typed one.
export function upstreamURL(format: string, current: string): string {
  if (current && !defaultUpstreamURLs.has(current)) {
    return current
  }
  return defaultUpstreams[format] ?? ''
}
