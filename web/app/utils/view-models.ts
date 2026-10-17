import type { Repository } from '~/gen/relic/v1/repository_pb'
import type { Permission } from '~/gen/relic/v1/security_pb'

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'unknown'

// severities lists them, most severe first.
export const severities: Severity[] = ['critical', 'high', 'medium', 'low', 'unknown']

// RepositoryRow is a repository as tables and selects list it.
export interface RepositoryRow {
  name: string
  class: 'local' | 'remote' | 'virtual'
  format: string
  url: string
  description: string
}

export function repositoryRow(r: Repository): RepositoryRow {
  return { name: r.name, class: repositoryClassName(r.class) || 'local', format: formatName(r.format), url: r.remote?.url ?? '', description: r.description }
}

// listRepoRows returns the repositories the caller may read, as rows.
export async function listRepoRows(): Promise<RepositoryRow[]> {
  return (await useRelic().repositories.listRepositories({})).repositories.map(repositoryRow)
}

// RepositoryView is a repository row.
export type RepositoryView = RepositoryRow

export interface ItemView {
  repo: string
  path: string
  name: string
  folder: boolean
  size: number
  sha1?: string
  sha256?: string
  md5?: string
  created: string
  createdBy: string
  modified: string
  modifiedBy: string
  vulns?: Partial<Record<Severity, number>>
}

export interface FindingView {
  id: string
  severity: Severity
  summary: string
  package: string
  version: string
  purl: string
  cves: string[]
  fixedVersions: string[]
  urls: string[]
  cvss: { version: string, vector?: string, score: number }[]
  malicious: boolean
  reachability: Reachability
  reachabilityTrace?: string
}

// Reachability is whether govulncheck found a call path to a Go
// vulnerability; unknown for everything else.
export type Reachability = 'reachable' | 'not_reachable' | 'unknown'

export const reachabilityBadges: Record<Exclude<Reachability, 'unknown'>, { label: string, color: 'error' | 'neutral', icon: string }> = {
  reachable: { label: 'Reachable', color: 'error', icon: 'i-lucide-route' },
  not_reachable: { label: 'Not reachable', color: 'neutral', icon: 'i-lucide-route-off' }
}

export interface ScanView {
  status: 'none' | 'queued' | 'done' | 'partial' | 'failed' | 'skipped'
  reason?: string
  components: number
  vulnerabilityDatabaseBuilt?: string
  counts: Partial<Record<Severity, number>>
  findings: FindingView[]
  issues: ScanIssueView[]
  risks: OperationalRiskView[]
  source?: string
}

export type OperationalRiskKind = 'end_of_life' | 'deprecated' | 'unmaintained'

export const operationalRiskKinds: Record<OperationalRiskKind, { icon: string, label: string }> = {
  end_of_life: { icon: 'i-lucide-calendar-x', label: 'End of life' },
  deprecated: { icon: 'i-lucide-archive-x', label: 'Deprecated' },
  unmaintained: { icon: 'i-lucide-hourglass', label: 'Unmaintained' }
}

// OperationalRiskView is a component past or near end of life, deprecated or
// unmaintained; repo and path name a file holding it, in listings.
export interface OperationalRiskView {
  kind: OperationalRiskKind
  severity: Severity
  purl: string
  package: string
  version: string
  product?: string
  cycle?: string
  eolFrom?: string
  detail: string
  repo?: string
  path?: string
}

// ScanIssueView is a secret or a misconfiguration found in scanned content;
// validation is a secret's, when its provider was asked.
export interface ScanIssueView {
  analyzer: 'secrets' | 'misconfiguration' | 'license' | 'model'
  rule: string
  severity: Severity
  title: string
  file: string
  line: number
  resource?: string
  validation?: 'live' | 'inactive' | 'unknown' | 'error'
}

export const secretValidations: Record<NonNullable<ScanIssueView['validation']>, { label: string, color: 'error' | 'success' | 'neutral' | 'warning' }> = {
  live: { label: 'Live', color: 'error' },
  inactive: { label: 'Inactive', color: 'success' },
  unknown: { label: 'Not confirmed', color: 'neutral' },
  error: { label: 'Check failed', color: 'warning' }
}

export interface ArtifactView {
  item: ItemView
  format: string
  image: boolean
  canDelete: boolean
  canAnnotate: boolean
  builds: { producedBy: BuildLinkView[], usedBy: BuildLinkView[] }
  downloads?: { count: number, last: string, lastDownloadedBy?: string }
  properties: Record<string, string[]>
  scan: ScanView
}

export interface BuildView {
  name: string
  lastStarted: string
}

export interface BuildRunView {
  number: string
  started: string
}

// Build info as clients publish it, the parts shown.
export interface BuildInfo {
  name: string
  number: string
  started: string
  agent?: { name: string, version: string }
  vcs?: { url?: string, revision?: string, branch?: string }[]
  modules?: {
    id: string
    type?: string
    artifacts?: { name: string, path?: string, type?: string, sha256?: string, sha1?: string }[]
    dependencies?: { id: string, type?: string, sha256?: string, sha1?: string }[]
  }[]
}

export interface GlobalFinding {
  id: string
  severity: Severity
  package: string
  version: string
  summary: string
  cves: string[]
  fixedVersions: string[]
  repo: string
  path: string
  epss?: number
  kev: boolean
  malicious: boolean
  reachability: Reachability
}

// CVE intelligence: facts from the CVE record, FIRST EPSS and CISA KEV.
export interface CveRow {
  id: string
  title: string
  severity: Severity
  published?: string
  cvss: { score?: number, vector?: string, version?: string, source?: string }
  epss?: number
  epssPercentile?: number
  kev: boolean
  ransomware: boolean
  exploitation?: 'none' | 'poc' | 'active'
  artifacts: number
  packages: number
}

export interface CveDetailView extends CveRow {
  description: string
  state?: string
  updated?: string
  cwes: string[]
  references: string[]
  automatable?: string
  technicalImpact?: string
  epssDate?: string
  kevEntry?: { added: string, due?: string, ransomware: boolean, action?: string }
  recordChecked?: string
  findings: {
    finding: string
    severity: Severity
    package: string
    version: string
    purl: string
    fixedVersions: string[]
    repo: string
    path: string
  }[]
}

export interface UserView {
  name: string
  email: string
  admin: boolean
  viewer: boolean
  disabled: boolean
  groups: string[]
}

export interface GroupView {
  name: string
  description: string
  defaultForNewUsers: boolean
  admin: boolean
  viewer: boolean
  members: string[]
}

export type PermissionAction = 'read' | 'annotate' | 'write' | 'delete' | 'manage' | 'promote'

// A permission's repository rule, as the permissions page edits it.
export interface PermissionView {
  name: string
  selector: string[]
  include: string[]
  exclude: string[]
  users: Record<string, PermissionAction[]>
  groups: Record<string, PermissionAction[]>
  nonRepositoryRules: ('build' | 'release')[]
  source: Permission
}

// The administration system page (GET /admin/system).
export interface SystemView {
  checks: { name: string, ok: boolean, error?: string, latencyMs: number }[]
  storage: { blobs: number, bytes: number, unreferencedBlobs: number, unreferencedBytes: number }
  jobs: JobKindView[]
  workers: { id: string, running: number, lastActive?: string, leader: boolean }[]
  queues: { name: string, updated?: string, paused: boolean, waiting: number, running: number }[]
  scanner: { vulnerabilityDatabaseBuilt?: string, scans: Partial<Record<ScanView['status'], number>> }
}

// River job states, as the job queue reports them.
export type JobState = 'available' | 'cancelled' | 'completed' | 'discarded' | 'pending' | 'retryable' | 'running' | 'scheduled'

export interface JobKindView {
  kind: string
  states: Partial<Record<JobState, number>>
  lastFinished?: string
  lastError?: string
  lastErrorAt?: string
}

// A cleanup policy (GET /admin/cleanup).
export interface CleanupPolicyView {
  name: string
  enabled: boolean
  repositories: string[]
  pathPattern: string
  olderThanDays: number
  notDownloadedDays: number
  keepLatest: number
  lastRun?: string
  lastMoved: number
  lastError?: string
}

// An item a cleanup policy selects.
export interface CleanupCandidateView {
  repository: string
  path: string
  size: number
  created: string
  lastDownloaded?: string
}

export type AuditOutcomeName = 'success' | 'denied' | 'failure'

// An audit log entry (GET /admin/audit).
export interface AuditEventView {
  id: string
  time: string
  actor: string
  action: string
  target: string
  outcome: AuditOutcomeName
  client: string
  detail?: string
}

// A push replication of a local repository (GET /admin/replications).
export interface ReplicationView {
  repository: string
  direction: 'push' | 'pull' | 'federated'
  targetUrl: string
  username: string
  hasPassword: boolean
  schedule: string
  enabled: boolean
  onChange: boolean
  syncDeletes: boolean
  syncProperties: boolean
  pathPrefix: string
  timeoutSeconds: number
  lastRun?: string
  lastStatus: 'never' | 'ok' | 'failed'
  lastError?: string
}

// The components of an item's stored SBOM (GET /sbom/{repo}/{path}).
export interface ComponentView {
  name: string
  version: string
  type: string
  purl?: string
  licenses: string[]
  licenseIds: string[]
  locations: { path: string, layerId?: string }[]
  violations: LicenseViolationView[]
}

// A license rule of a watch covering the item's repository that a
// component violates.
export interface LicenseViolationView {
  license: string
  watch: string
  policy: string
  rule: string
  failBuild: boolean
  blockDownload: boolean
}

export interface SbomView {
  source?: string
  components: ComponentView[]
}

// The image a Docker tag folder holds (GET /docker/{repo}/{path}).
export interface ImageInfo {
  digest: string
  mediaType: string
  platform?: string
  created?: string
  totalSize: number
  labels: Record<string, string>
  layers: { digest: string, size: number, mediaType: string, instruction?: string, created?: string }[]
  platforms?: { digest: string, os: string, architecture: string, variant?: string, size: number }[]
}

// package.json facts of an npm tarball (GET /npm/{repo}/{path}).
export interface NpmPackageInfo {
  name: string
  version: string
  description?: string
  license?: string
  homepage?: string
  repository?: string
  keywords: string[]
  dependencies: Record<string, string>
  devDependencies: Record<string, string>
  peerDependencies: Record<string, string>
}

// One deleted item in the trash can (GET /admin/trash).
export interface TrashEntryView {
  path: string
  name: string
  folder: boolean
  size: number
  originalRepo: string
  originalPath: string
  deletedBy: string
  deleted: string
}

// A build run that produced or used a file.
export interface BuildLinkView {
  name: string
  number: string
  started: string
  module: string
  scopes?: string
}

// Who may do what on an item (GET /effective-permissions/{repo}/{path}).
export interface GrantView {
  principal: string
  group: boolean
  actions: PermissionAction[]
  permissions: string[]
}

// An archive's entries (GET /archive/{repo}/{path}).
export interface ArchiveView {
  kind: 'zip' | 'tar.gz' | 'tar'
  entries: { path: string, size: number, modified: string, folder: boolean }[]
  truncated: boolean
}

// Package metadata (GET /package/{repo}/{path}): a wheel/sdist or a chart.
export interface PackageInfoView {
  kind: 'pypi' | 'helm'
  pypi?: {
    name: string
    version: string
    summary?: string
    license?: string
    requiresPython?: string
    author?: string
    homePage?: string
    projectUrls: string[]
    requiresDist: string[]
    classifiers: string[]
  }
  helm?: {
    name: string
    version: string
    appVersion?: string
    description?: string
    type?: string
    apiVersion?: string
    kubeVersion?: string
    home?: string
    deprecated?: boolean
    keywords: string[]
    sources: string[]
    maintainers: Record<string, string>[]
    dependencies: Record<string, string>[]
  }
}

export type ReleaseStatusName = 'processing' | 'completed' | 'draft' | 'failed' | 'unknown'

export interface ReleaseView {
  project: string
  name: string
  version: string
  repository: string
  manifest: string
  status: ReleaseStatusName
  tag: string
  artifacts: { repository: string, path: string, sha256: string, size: number }[]
  createdBy: string
  created: string
  versions: number
}

export interface ReleasePromotionView {
  environment: string
  status: ReleaseStatusName
  include: string[]
  exclude: string[]
  messages: string[]
  createdBy: string
  created: string
}

export interface ReleaseEvidenceView {
  id: string
  predicateType: string
  verified: boolean
  createdBy: string
  created: string
  // gate is set for a promotion gate's decision.
  gate?: { name: string, environment: string, allowed: boolean, error?: string }
}

export interface PolicyDecisionView {
  kind: string
  subject: string
  policy: string
  watch?: string
  rule?: string
  reason: string
  actor: string
  count: number
  lastSeen?: string
}
