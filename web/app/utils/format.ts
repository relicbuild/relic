import type { Severity } from '~/utils/view-models'

const units = ['B', 'KB', 'MB', 'GB', 'TB']

// formatBytes renders a size with a binary unit, e.g. 1.5 MB.
export function formatBytes(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`
}

// formatDate renders a timestamp in the viewer's locale.
export function formatDate(iso?: string): string {
  return iso ? new Date(iso).toLocaleString() : ''
}

// formatDay renders a timestamp's date in the viewer's locale.
export function formatDay(iso?: string): string {
  return iso ? new Date(iso).toLocaleDateString() : ''
}

// shellWord is value as one shell word, single-quoted unless it is plain or a <placeholder>.
export function shellWord(value: string): string {
  return /^([\w@%+=:,./-]+|<\w+>)$/.test(value) ? value : `'${value.replaceAll('\'', '\'\\\'\'')}'`
}

// severityColor maps a severity to a semantic color.
export function severityColor(severity: Severity): 'error' | 'warning' | 'info' | 'neutral' {
  switch (severity) {
    case 'critical':
    case 'high':
      return 'error'
    case 'medium':
      return 'warning'
    case 'low':
      return 'info'
    default:
      return 'neutral'
  }
}

// severityLabel is a severity as the UI shows it: "Critical".
export function severityLabel(severity: Severity): string {
  return severity.charAt(0).toUpperCase() + severity.slice(1)
}

// pluralize renders a count with a noun, e.g. "1 finding", "2 findings",
// "2 vulnerabilities", "2 matches".
export function pluralize(n: number, noun: string): string {
  if (n === 1) {
    return `${n} ${noun}`
  }
  if (/[^aeiou]y$/.test(noun)) {
    return `${n} ${noun.slice(0, -1)}ies`
  }
  return `${n} ${noun}${/(s|x|ch|sh)$/.test(noun) ? 'es' : 's'}`
}

const textExtensions = new Set([
  'pom', 'xml', 'json', 'jsonc', 'txt', 'md', 'rst', 'adoc', 'yaml', 'yml', 'properties', 'toml', 'cfg', 'conf', 'ini',
  'sha1', 'sha256', 'sha512', 'md5', 'asc', 'sig', 'module', 'gradle', 'kts', 'mod', 'info', 'nuspec', 'gemspec', 'spec',
  'csv', 'tsv', 'log', 'sh', 'bash', 'ps1', 'bat', 'cmd', 'py', 'pyi', 'js', 'mjs', 'cjs', 'jsx', 'ts', 'tsx', 'vue',
  'go', 'java', 'kt', 'scala', 'groovy', 'rs', 'c', 'h', 'cpp', 'hpp', 'cs', 'swift', 'rb', 'php', 'sql', 'proto',
  'graphql', 'tf', 'hcl', 'nix', 'cmake', 'lock', 'sum', 'mf', 'html', 'css', 'svg', 'pem', 'crt', 'repo', 'list'
])

// isViewableInBrowser reports whether a file is text the View tab can show.
export function isViewableInBrowser(name: string, size: number): boolean {
  const ext = name.includes('.') ? name.split('.').pop()!.toLowerCase() : name.toLowerCase()
  return size <= 1 << 20 && (textExtensions.has(ext) || ['license', 'readme', 'notice', 'dockerfile', 'makefile'].includes(ext))
}

// Maven coordinates from a repository path laid out as
// group/artifact/version/artifact-version[-classifier].ext.
export interface MavenCoordinates { group: string, artifact: string, version: string, classifier?: string, ext: string }
export function mavenCoordinates(path: string): MavenCoordinates | undefined {
  const parts = path.split('/')
  if (parts.length < 4) {
    return undefined
  }
  const [artifact, version, file] = parts.slice(-3) as [string, string, string]
  const prefix = `${artifact}-${version}`
  if (!file.startsWith(prefix)) {
    return undefined
  }
  const rest = file.slice(prefix.length)
  const dot = rest.indexOf('.')
  if (dot < 0 || (dot > 0 && !rest.startsWith('-'))) {
    return undefined
  }
  return {
    group: parts.slice(0, -3).join('.'),
    artifact,
    version,
    classifier: dot > 0 ? rest.slice(1, dot) : undefined,
    ext: rest.slice(dot + 1)
  }
}

// isArchive mirrors the server's archive kinds (internal/artifacts/archive.KindOf).
export function isArchive(name: string): boolean {
  return /\.(zip|jar|war|ear|aar|whl|nupkg|egg|vsix|apk|tar\.gz|tgz|crate|tar|gem)$/i.test(name)
}

// formatPercent renders a 0..1 probability as a percentage.
export function formatPercent(p?: number): string {
  if (p === undefined) return '—'
  return p >= 0.1 ? `${(p * 100).toFixed(1)}%` : `${(p * 100).toFixed(2)}%`
}

// epssColor ranks an EPSS probability: 10% and up is likely exploited.
export function epssColor(p?: number): 'error' | 'warning' | 'neutral' {
  if (p === undefined) return 'neutral'
  return p >= 0.1 ? 'error' : p >= 0.01 ? 'warning' : 'neutral'
}
