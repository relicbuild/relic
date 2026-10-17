// Every name is a literal so the icon client bundle (nuxt.config: icon.clientBundle.scan) includes it.

const formatIcons: Record<string, string> = {
  maven: 'i-material-icon-theme-maven',
  gradle: 'i-logos-gradle',
  npm: 'i-logos-npm-icon',
  pypi: 'i-logos-python',
  gomod: 'i-material-icon-theme-go',
  docker: 'i-logos-docker-icon',
  oci: 'i-logos-docker-icon',
  helm: 'i-logos-helm',
  helmoci: 'i-logos-helm',
  nuget: 'i-material-icon-theme-nuget',
  rubygems: 'i-logos-rubygems',
  cargo: 'i-material-icon-theme-rust',
  conan: 'i-logos-conan-io',
  terraform: 'i-logos-terraform-icon',
  huggingface: 'i-logos-hugging-face-icon',
  alpine: 'i-simple-icons-alpinelinux',
  debian: 'i-logos-debian',
  rpm: 'i-simple-icons-redhat',
  nix: 'i-material-icon-theme-nix',
  mcp: 'i-lucide-plug',
  skills: 'i-lucide-sparkles'
}

/** The logo of a repository's format; a plain box for generic ones. */
export function formatIcon(format: string | undefined): string {
  return formatIcons[format ?? ''] ?? 'i-lucide-box'
}

const repositoryClassIcons: Record<string, string> = {
  local: 'i-lucide-hard-drive',
  remote: 'i-lucide-cloud-download',
  virtual: 'i-lucide-combine'
}

/** The semantic colour of a repository class (text-* utility). */
export function repositoryClassColor(repositoryClass: string | undefined): string {
  return ({ local: 'text-primary', remote: 'text-info', virtual: 'text-success' } as Record<string, string>)[repositoryClass ?? ''] ?? 'text-muted'
}

/** The icon of a repository class: local, remote or virtual. */
export function repositoryClassIcon(repositoryClass: string | undefined): string {
  return repositoryClassIcons[repositoryClass ?? ''] ?? 'i-lucide-database'
}

const fileNameIcons: Record<string, string> = {
  'manifest.json': 'i-material-icon-theme-docker',
  'list.manifest.json': 'i-material-icon-theme-docker',
  'package.json': 'i-material-icon-theme-npm',
  'go.mod': 'i-material-icon-theme-go-mod',
  'go.sum': 'i-material-icon-theme-go-mod',
  'maven-metadata.xml': 'i-material-icon-theme-maven',
  'pom.xml': 'i-material-icon-theme-maven',
  'index.yaml': 'i-material-icon-theme-helm',
  'chart.yaml': 'i-material-icon-theme-helm',
  'cargo.toml': 'i-material-icon-theme-rust',
  'gemfile': 'i-material-icon-theme-gemfile',
  'license': 'i-material-icon-theme-license',
  'readme.md': 'i-material-icon-theme-readme',
  'dockerfile': 'i-material-icon-theme-docker'
}

const extensionIcons: [string, string][] = [
  ['.tar.gz', 'i-material-icon-theme-zip'],
  ['.tar.bz2', 'i-material-icon-theme-zip'],
  ['.tar.xz', 'i-material-icon-theme-zip'],
  ['.cyclonedx.json', 'i-material-icon-theme-verified'],
  ['.spdx.json', 'i-material-icon-theme-verified'],
  ['.jar', 'i-material-icon-theme-jar'],
  ['.war', 'i-material-icon-theme-jar'],
  ['.ear', 'i-material-icon-theme-jar'],
  ['.aar', 'i-material-icon-theme-jar'],
  ['.pom', 'i-material-icon-theme-maven'],
  ['.module', 'i-material-icon-theme-gradle'],
  ['.tgz', 'i-material-icon-theme-npm'],
  ['.whl', 'i-material-icon-theme-python'],
  ['.egg', 'i-material-icon-theme-python'],
  ['.nupkg', 'i-material-icon-theme-nuget'],
  ['.nuspec', 'i-material-icon-theme-nuget'],
  ['.crate', 'i-material-icon-theme-rust'],
  ['.gem', 'i-material-icon-theme-ruby'],
  ['.deb', 'i-simple-icons-debian'],
  ['.apk', 'i-simple-icons-alpinelinux'],
  ['.nar', 'i-material-icon-theme-nix'],
  ['.narinfo', 'i-material-icon-theme-nix'],
  ['.safetensors', 'i-simple-icons-huggingface'],
  ['.zip', 'i-material-icon-theme-zip'],
  ['.gz', 'i-material-icon-theme-zip'],
  ['.xz', 'i-material-icon-theme-zip'],
  ['.tar', 'i-material-icon-theme-zip'],
  ['.7z', 'i-material-icon-theme-zip'],
  ['.json', 'i-material-icon-theme-json'],
  ['.yaml', 'i-material-icon-theme-yaml'],
  ['.yml', 'i-material-icon-theme-yaml'],
  ['.toml', 'i-material-icon-theme-toml'],
  ['.xml', 'i-material-icon-theme-xml'],
  ['.properties', 'i-material-icon-theme-settings'],
  ['.ini', 'i-material-icon-theme-settings'],
  ['.md', 'i-material-icon-theme-markdown'],
  ['.txt', 'i-material-icon-theme-document'],
  ['.log', 'i-material-icon-theme-log'],
  ['.html', 'i-material-icon-theme-html'],
  ['.css', 'i-material-icon-theme-css'],
  ['.js', 'i-material-icon-theme-javascript'],
  ['.ts', 'i-material-icon-theme-typescript'],
  ['.py', 'i-material-icon-theme-python'],
  ['.go', 'i-material-icon-theme-go'],
  ['.java', 'i-material-icon-theme-java'],
  ['.kt', 'i-material-icon-theme-kotlin'],
  ['.scala', 'i-material-icon-theme-scala'],
  ['.rs', 'i-material-icon-theme-rust'],
  ['.rb', 'i-material-icon-theme-ruby'],
  ['.cs', 'i-material-icon-theme-csharp'],
  ['.c', 'i-material-icon-theme-c'],
  ['.h', 'i-material-icon-theme-h'],
  ['.cpp', 'i-material-icon-theme-cpp'],
  ['.proto', 'i-material-icon-theme-proto'],
  ['.sh', 'i-material-icon-theme-console'],
  ['.ps1', 'i-material-icon-theme-powershell'],
  ['.sql', 'i-material-icon-theme-database'],
  ['.tf', 'i-material-icon-theme-terraform'],
  ['.wasm', 'i-material-icon-theme-webassembly'],
  ['.ipynb', 'i-material-icon-theme-jupyter'],
  ['.pdf', 'i-material-icon-theme-pdf'],
  ['.svg', 'i-material-icon-theme-svg'],
  ['.png', 'i-material-icon-theme-image'],
  ['.jpg', 'i-material-icon-theme-image'],
  ['.jpeg', 'i-material-icon-theme-image'],
  ['.gif', 'i-material-icon-theme-image'],
  ['.crt', 'i-material-icon-theme-certificate'],
  ['.pem', 'i-material-icon-theme-certificate'],
  ['.key', 'i-material-icon-theme-key'],
  ['.lock', 'i-material-icon-theme-lock'],
  ['.asc', 'i-lucide-signature'],
  ['.sig', 'i-lucide-signature'],
  ['.sha1', 'i-lucide-hash'],
  ['.sha256', 'i-lucide-hash'],
  ['.sha512', 'i-lucide-hash'],
  ['.md5', 'i-lucide-hash'],
  ['.exe', 'i-material-icon-theme-exe'],
  ['.so', 'i-material-icon-theme-lib'],
  ['.dll', 'i-material-icon-theme-lib'],
  ['.bin', 'i-material-icon-theme-hex']
]

/**
 * The icon of a file or folder in the repository tree.
 */
export function fileIcon(name: string, folder: boolean): string | undefined {
  const lower = name.toLowerCase()
  if (lower.startsWith('sha256__') || lower.startsWith('sha256:')) {
    return 'i-lucide-layers'
  }
  if (folder) {
    return undefined
  }
  const byName = fileNameIcons[lower]
  if (byName) {
    return byName
  }
  for (const [ext, icon] of extensionIcons) {
    if (lower.endsWith(ext)) {
      return icon
    }
  }
  return 'i-lucide-file'
}
