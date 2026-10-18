import type { ArchiveEntry, BuildLink, EffectiveGrant, GetItemDetailsResponse, Item, ListArchiveEntriesResponse } from '~/gen/relic/v1/artifact_pb'
import { ScanStatus, Severity as SeverityEnum, type Cve, type CveSummary, type Finding, type GetItemSbomResponse, type ItemScan, type FindingOccurrence, type OperationalRisk } from '~/gen/relic/v1/scan_pb'
import type { GetBuildRunResponse } from '~/gen/relic/v1/build_pb'
import { Action, RepositorySet, ResourceKind, type Permission, type PermissionRule, type PermissionRuleSchema } from '~/gen/relic/v1/security_pb'
import type { MessageInitShape } from '@bufbuild/protobuf'
import { ReplicationDirection, ReplicationStatus, type Replication } from '~/gen/relic/v1/replication_pb'
import type { CleanupCandidate, CleanupPolicy } from '~/gen/relic/v1/cleanup_pb'
import { AuditOutcome, type AuditEvent } from '~/gen/relic/v1/audit_pb'
import type { GetSystemStatusResponse } from '~/gen/relic/v1/system_pb'
import type { TrashEntry } from '~/gen/relic/v1/trash_pb'
import { ReleaseStatus, type Release, type ReleasePromotion } from '~/gen/relic/v1/release_pb'
import type { Evidence } from '~/gen/relic/v1/evidence_pb'
import { PolicyDecisionKind, type PolicyDecision } from '~/gen/relic/v1/policy_decision_pb'
import type { GetContainerImageResponse, GetNpmPackageResponse } from '~/gen/relic/v1/package_pb'
import type { ReleaseEvidenceView, ReleasePromotionView, ReleaseStatusName, ReleaseView, AuditEventView, AuditOutcomeName, BuildInfo, CleanupCandidateView, CleanupPolicyView, CveDetailView, CveRow, GlobalFinding, PackageInfoView, ArchiveView, ArtifactView, BuildLinkView, ComponentView, FindingView, GrantView, ImageInfo, OperationalRiskKind, OperationalRiskView, Reachability, ItemView, NpmPackageInfo, PermissionAction, PermissionView, PolicyDecisionView, ReplicationView, SbomView, ScanIssueView, ScanView, Severity, SystemView, TrashEntryView } from '~/utils/view-models'

export function itemView(it: Item): ItemView {
  return {
    repo: it.repository, path: it.path, name: it.name, folder: it.folder, size: toNumber(it.size),
    sha1: it.checksums?.sha1, sha256: it.checksums?.sha256, md5: it.checksums?.md5,
    created: toIsoDate(it.created), createdBy: it.createdBy, modified: toIsoDate(it.modified), modifiedBy: it.modifiedBy,
    vulns: it.vulnerabilities as ItemView['vulns']
  }
}

// severityName is a Severity as the view models name it.
export function severityName(s: SeverityEnum): Severity {
  return (enumName(SeverityEnum, s) || 'unknown') as Severity
}

// findingSeverityOf is the Severity enum value of a severity name ('' for none).
export function findingSeverityOf(name: string): SeverityEnum {
  return enumValue(SeverityEnum, name) as SeverityEnum
}

export function findingView(f: Finding): FindingView {
  return {
    id: f.id, severity: severityName(f.severity), summary: f.summary, package: f.packageName, version: f.packageVersion, purl: f.purl,
    cves: f.aliases, fixedVersions: f.fixedVersions, urls: f.urls, cvss: f.cvss.map(c => ({ version: c.version, vector: c.vector, score: c.score })),
    malicious: f.malicious, reachability: (f.reachability || 'unknown') as Reachability, reachabilityTrace: f.reachabilityTrace || undefined
  }
}

export function scanView(s?: ItemScan): ScanView {
  return {
    status: (enumName(ScanStatus, s?.status ?? ScanStatus.NONE) || 'none') as ScanView['status'], reason: s?.reason || undefined, components: s?.components ?? 0,
    vulnerabilityDatabaseBuilt: s?.vulnerabilityDatabaseBuilt ? toIsoDate(s.vulnerabilityDatabaseBuilt) : undefined, counts: (s?.counts ?? {}) as ScanView['counts'],
    findings: (s?.findings ?? []).map(findingView), source: s?.source || undefined,
    issues: (s?.issues ?? []).map(i => ({
      analyzer: i.analyzer as ScanIssueView['analyzer'], rule: i.rule, severity: severityName(i.severity), title: i.title,
      file: i.file, line: i.line, resource: i.resource || undefined,
      validation: (i.validation || undefined) as ScanIssueView['validation']
    })),
    risks: (s?.risks ?? []).map(operationalRiskView)
  }
}

export function operationalRiskView(r: OperationalRisk): OperationalRiskView {
  return {
    kind: r.kind as OperationalRiskKind, severity: severityName(r.severity), purl: r.purl, package: r.packageName,
    version: r.packageVersion, product: r.product || undefined, cycle: r.cycle || undefined, eolFrom: r.eolFrom || undefined,
    detail: r.detail, repo: r.repository || undefined, path: r.path || undefined
  }
}

function buildLinkView(l: BuildLink): BuildLinkView {
  return { name: l.name, number: l.number, project: l.project, started: toIsoDate(l.started), module: l.module, scopes: l.scopes || undefined }
}

export function artifactView(d: GetItemDetailsResponse): ArtifactView {
  const props: Record<string, string[]> = {}
  for (const [k, v] of Object.entries(d.properties)) {
    props[k] = v.values
  }
  return {
    item: itemView(d.item!), format: formatName(d.format), image: d.image, canDelete: d.canDelete, canAnnotate: d.canAnnotate,
    builds: { producedBy: d.producedBy.map(buildLinkView), usedBy: d.usedBy.map(buildLinkView) },
    downloads: d.downloads ? { count: toNumber(d.downloads.count), last: toIsoDate(d.downloads.lastDownloadedAt), lastDownloadedBy: d.downloads.lastDownloadedBy || undefined } : undefined,
    properties: props, scan: scanView(d.scan)
  }
}

export function sbomView(s: GetItemSbomResponse): SbomView {
  return {
    source: s.source || undefined,
    components: s.components.map((c): ComponentView => ({
      name: c.name, version: c.version, type: c.type, purl: c.purl || undefined, licenses: c.licenses,
      licenseIds: c.licenseIds,
      locations: c.locations.map(l => ({ path: l.path, layerId: l.layer || undefined })),
      violations: c.violations.map(v => ({
        license: v.license, watch: v.watch, policy: v.policy, rule: v.rule,
        failBuild: v.failBuild, blockDownload: v.blockDownload
      }))
    }))
  }
}

// actionName names an action the way Relic does (ACTION_READ is read).
export function actionName(a: Action): PermissionAction {
  return (Action[a] ?? '').toLowerCase() as PermissionAction
}

export function permissionActionOf(name: PermissionAction): Action {
  return Action[name.toUpperCase() as keyof typeof Action]
}

export function grantView(g: EffectiveGrant): GrantView {
  return { principal: g.principal, group: g.group, actions: g.actions.map(actionName), permissions: g.permissions }
}

export function archiveView(a: ListArchiveEntriesResponse): ArchiveView {
  return {
    kind: a.kind as ArchiveView['kind'], truncated: a.truncated,
    entries: a.entries.map((e: ArchiveEntry) => ({ path: e.path, size: toNumber(e.size), modified: toIsoDate(e.modified), folder: e.folder }))
  }
}

export function imageInfo(i: GetContainerImageResponse): ImageInfo {
  return {
    digest: i.digest, mediaType: i.mediaType, platform: [i.os, i.architecture].filter(Boolean).join('/') || undefined,
    created: i.created ? toIsoDate(i.created) : undefined, totalSize: toNumber(i.totalSize), labels: i.labels,
    layers: i.layers.map(l => ({ digest: l.digest, size: toNumber(l.size), mediaType: l.mediaType, instruction: l.instruction || undefined, created: l.created ? toIsoDate(l.created) : undefined })),
    platforms: i.platforms.length ? i.platforms.map(p => ({ digest: p.digest, os: p.os, architecture: p.architecture, variant: p.variant || undefined, size: toNumber(p.size) })) : undefined
  }
}

export function npmInfo(n: GetNpmPackageResponse): NpmPackageInfo {
  return {
    name: n.name, version: n.version, description: n.description || undefined, license: n.license || undefined,
    homepage: n.homepage || undefined, repository: n.repository || undefined, keywords: n.keywords,
    dependencies: n.dependencies, devDependencies: n.devDependencies, peerDependencies: n.peerDependencies
  }
}

// packageInfo reads a Python package's or Helm chart's metadata, whichever
// the file is; NotFound when it is neither.
export async function packageInfo(repository: string, path: string): Promise<PackageInfoView> {
  const { packages } = useRelic()
  try {
    const p = await packages.getPythonPackage({ repository, path })
    return {
      kind: 'pypi',
      pypi: {
        name: p.name, version: p.version, summary: p.summary || undefined, license: p.license || undefined, requiresPython: p.requiresPython || undefined,
        author: p.author || undefined, homePage: p.homePage || undefined, projectUrls: p.projectUrls, requiresDist: p.requiresDist, classifiers: p.classifiers
      }
    }
  } catch (err) {
    if (!isNotFound(err)) {
      throw err
    }
  }
  const h = await packages.getHelmChart({ repository, path })
  return {
    kind: 'helm',
    helm: {
      name: h.name, version: h.version, appVersion: h.appVersion || undefined, description: h.description || undefined, type: h.type || undefined,
      apiVersion: h.apiVersion || undefined, kubeVersion: h.kubeVersion || undefined, home: h.home || undefined,
      deprecated: h.deprecated || undefined, keywords: h.keywords, sources: h.sources,
      maintainers: h.maintainers.map(m => m.fields), dependencies: h.dependencies.map(d => d.fields)
    }
  }
}

// buildInfo is a build run as the build page shows it.
export function buildInfo(r: GetBuildRunResponse): BuildInfo {
  return {
    name: r.name, number: r.number, started: toIsoDate(r.started),
    agent: r.agent ? { name: r.agent, version: '' } : undefined,
    vcs: r.vcs.map(v => ({ url: v.url || undefined, revision: v.revision || undefined, branch: v.branch || undefined })),
    modules: r.modules.map(m => ({
      id: m.id, type: m.type || undefined,
      artifacts: m.artifacts.map(a => ({ name: a.name, path: a.path || undefined, type: a.type || undefined, sha256: a.sha256 || undefined, sha1: a.sha1 || undefined })),
      dependencies: m.dependencies.map(d => ({ id: d.id, type: d.type || undefined, sha256: d.sha256 || undefined, sha1: d.sha1 || undefined }))
    }))
  }
}

const setNames: [RepositorySet, string][] = [[RepositorySet.ALL, '@all'], [RepositorySet.LOCAL, '@local'], [RepositorySet.REMOTE, '@remote']]

export function permissionView(p: Permission): PermissionView {
  const r = p.rules.find(r => r.resource === ResourceKind.REPOSITORY)
  const users: Record<string, PermissionAction[]> = {}
  const groups: Record<string, PermissionAction[]> = {}
  for (const g of r?.grants ?? []) {
    const actions = g.actions.map(actionName)
    if (g.principal.case === 'user') {
      users[g.principal.value] = actions
    } else if (g.principal.case === 'group') {
      groups[g.principal.value] = actions
    }
  }
  return {
    name: p.name,
    selector: [
      ...setNames.filter(([s]) => r?.selector?.sets.includes(s)).map(([, n]) => n),
      ...(r?.selector?.repositories ?? [])
    ],
    include: r?.include ?? [], exclude: r?.exclude ?? [], users, groups,
    nonRepositoryRules: p.rules.flatMap(o => o.resource === ResourceKind.BUILD ? ['build' as const] : o.resource === ResourceKind.RELEASE ? ['release' as const] : []),
    source: p
  }
}

// repositoryRule builds a permission's repository rule from the page's
// fields; selector entries starting with @ are repository sets.
export function repositoryRule(selector: string[], include: string[], exclude: string[],
  users: Record<string, PermissionAction[]>, groups: Record<string, PermissionAction[]>): MessageInitShape<typeof PermissionRuleSchema> {
  return {
    resource: ResourceKind.REPOSITORY,
    selector: {
      sets: setNames.filter(([, n]) => selector.includes(n)).map(([s]) => s),
      repositories: selector.filter(s => !s.startsWith('@'))
    },
    include, exclude,
    grants: [
      ...Object.entries(users).map(([user, actions]) => ({ principal: { case: 'user' as const, value: user }, actions: actions.map(permissionActionOf) })),
      ...Object.entries(groups).map(([group, actions]) => ({ principal: { case: 'group' as const, value: group }, actions: actions.map(permissionActionOf) }))
    ]
  }
}

// nonRepositoryRules are a permission's rules other than its repository rule, kept
// when the page saves it.
export function nonRepositoryRules(p?: Permission): PermissionRule[] {
  return (p?.rules ?? []).filter(r => r.resource !== ResourceKind.REPOSITORY)
}

export function replicationView(r: Replication): ReplicationView {
  return {
    repository: r.repository, direction: (enumName(ReplicationDirection, r.direction) || 'push') as ReplicationView['direction'],
    targetUrl: r.targetUrl, username: r.username, hasPassword: r.hasPassword, schedule: r.schedule, enabled: r.enabled,
    onChange: r.onChange, syncDeletes: r.syncDeletes, syncProperties: r.syncProperties, pathPrefix: r.pathPrefix, timeoutSeconds: r.timeoutSeconds,
    lastRun: r.lastRun ? toIsoDate(r.lastRun) : undefined, lastStatus: (enumName(ReplicationStatus, r.lastStatus) || 'never') as ReplicationView['lastStatus'],
    lastError: r.lastError || undefined
  }
}

const auditOutcomeNames: Record<AuditOutcome, AuditOutcomeName> = {
  [AuditOutcome.UNSPECIFIED]: 'failure',
  [AuditOutcome.SUCCESS]: 'success',
  [AuditOutcome.DENIED]: 'denied',
  [AuditOutcome.FAILURE]: 'failure'
}

export const auditOutcomeOf = (name: AuditOutcomeName): AuditOutcome =>
  ({ success: AuditOutcome.SUCCESS, denied: AuditOutcome.DENIED, failure: AuditOutcome.FAILURE })[name]

export function auditEventView(e: AuditEvent): AuditEventView {
  return {
    id: e.id, time: e.time ? toIsoDate(e.time) : '', actor: e.actor, action: e.action, target: e.target,
    outcome: auditOutcomeNames[e.outcome], client: e.client, detail: e.detail || undefined
  }
}

export function cleanupPolicyView(p: CleanupPolicy): CleanupPolicyView {
  return {
    name: p.name, enabled: p.enabled, repositories: p.repositories, pathPattern: p.pathPattern,
    olderThanDays: p.olderThanDays, notDownloadedDays: p.notDownloadedDays, keepLatest: p.keepLatest,
    lastRun: p.lastRun ? toIsoDate(p.lastRun) : undefined, lastMoved: toNumber(p.lastMoved), lastError: p.lastError || undefined
  }
}

export function cleanupCandidateView(c: CleanupCandidate): CleanupCandidateView {
  return {
    repository: c.repository, path: c.path, size: toNumber(c.size), created: toIsoDate(c.created),
    lastDownloaded: c.lastDownloaded ? toIsoDate(c.lastDownloaded) : undefined
  }
}

// cleanupCriteria summarizes what a cleanup policy selects, e.g.
// "older than 30 days · keep 5 latest".
export function cleanupCriteria(p: Pick<CleanupPolicyView, 'olderThanDays' | 'notDownloadedDays' | 'keepLatest'>): string {
  return [
    p.olderThanDays ? `older than ${pluralize(p.olderThanDays, 'day')}` : '',
    p.notDownloadedDays ? `not downloaded for ${pluralize(p.notDownloadedDays, 'day')}` : '',
    p.keepLatest ? `keep ${p.keepLatest} latest` : ''
  ].filter(Boolean).join(' · ')
}

export function systemView(s: GetSystemStatusResponse): SystemView {
  return {
    checks: s.checks.map(c => ({ name: c.name, ok: c.ok, error: c.error || undefined, latencyMs: toNumber(c.latencyMs) })),
    storage: {
      blobs: toNumber(s.storage?.blobs), bytes: toNumber(s.storage?.bytes),
      unreferencedBlobs: toNumber(s.storage?.unreferencedBlobs), unreferencedBytes: toNumber(s.storage?.unreferencedBytes)
    },
    jobs: s.backgroundJobs.map(j => ({
      kind: j.kind, states: j.states, lastFinished: j.lastFinished ? toIsoDate(j.lastFinished) : undefined,
      lastError: j.lastError || undefined, lastErrorAt: j.lastErrorAt ? toIsoDate(j.lastErrorAt) : undefined
    })),
    workers: s.workers.map(w => ({ id: w.id, running: w.running, lastActive: w.lastActive ? toIsoDate(w.lastActive) : undefined, leader: w.leader })),
    queues: s.queues.map(q => ({ name: q.name, updated: q.updated ? toIsoDate(q.updated) : undefined, paused: q.paused, waiting: q.waiting, running: q.running })),
    scanner: { vulnerabilityDatabaseBuilt: s.scanner?.vulnerabilityDatabaseBuilt ? toIsoDate(s.scanner.vulnerabilityDatabaseBuilt) : undefined, scans: s.scanner?.scans ?? {} }
  }
}

export function trashEntryView(e: TrashEntry): TrashEntryView {
  return {
    path: e.path, name: e.name, folder: e.folder, size: toNumber(e.size), originalRepo: e.originalRepository,
    originalPath: e.originalPath, deletedBy: e.deletedBy, deleted: toIsoDate(e.deleted)
  }
}

export function globalFinding(f: FindingOccurrence): GlobalFinding {
  return {
    id: f.id, severity: severityName(f.severity), package: f.packageName, version: f.packageVersion, summary: f.summary,
    cves: f.cves, fixedVersions: f.fixedVersions, repo: f.repository, path: f.path, epss: f.epss, kev: f.kev, malicious: f.malicious,
    reachability: (f.reachability || 'unknown') as Reachability
  }
}

export function cveRowView(c?: CveSummary): CveRow {
  return {
    id: c?.id ?? '', title: c?.title ?? '', severity: severityName(c?.severity ?? SeverityEnum.UNKNOWN), published: c?.published ? toIsoDate(c.published) : undefined,
    cvss: { score: c?.cvssScore, version: c?.cvssVersion || undefined }, epss: c?.epss, epssPercentile: c?.epssPercentile,
    kev: !!c?.kev, ransomware: !!c?.ransomware, exploitation: (c?.exploitation || undefined) as CveRow['exploitation'],
    artifacts: toNumber(c?.artifacts), packages: toNumber(c?.packages)
  }
}

export function cveDetailView(c: Cve): CveDetailView {
  const row = cveRowView(c.summary)
  return {
    ...row,
    cvss: { ...row.cvss, vector: c.cvssVector || undefined, source: c.cvssSource || undefined },
    description: c.description, state: c.state || undefined, updated: c.updated ? toIsoDate(c.updated) : undefined,
    cwes: c.cwes, references: c.references, automatable: c.automatable || undefined, technicalImpact: c.technicalImpact || undefined,
    epssDate: c.epssDate ? toIsoDate(c.epssDate) : undefined,
    kevEntry: c.kevAdded ? { added: toIsoDate(c.kevAdded), due: c.kevDue ? toIsoDate(c.kevDue) : undefined, ransomware: row.ransomware, action: c.kevAction || undefined } : undefined,
    recordChecked: c.recordChecked ? toIsoDate(c.recordChecked) : undefined,
    findings: c.affected.map(a => ({
      finding: a.finding, severity: severityName(a.severity), package: a.packageName, version: a.packageVersion, purl: a.purl,
      fixedVersions: a.fixedVersions, repo: a.repository, path: a.path
    }))
  }
}

const releaseStatusNames: Record<ReleaseStatus, ReleaseStatusName> = {
  [ReleaseStatus.UNSPECIFIED]: 'unknown',
  [ReleaseStatus.PROCESSING]: 'processing',
  [ReleaseStatus.COMPLETED]: 'completed',
  [ReleaseStatus.DRAFT]: 'draft',
  [ReleaseStatus.FAILED]: 'failed'
}

export function releaseView(r: Release): ReleaseView {
  return {
    project: r.project, name: r.name, version: r.version, repository: r.repository, manifest: r.manifest,
    status: releaseStatusNames[r.status], tag: r.tag,
    artifacts: r.artifacts.map(a => ({ repository: a.repository, path: a.path, sha256: a.sha256, size: toNumber(a.size) })),
    createdBy: r.createdBy, created: r.created ? toIsoDate(r.created) : '', versions: toNumber(r.versions)
  }
}

export function releasePromotionView(p: ReleasePromotion): ReleasePromotionView {
  return {
    environment: p.environment, status: releaseStatusNames[p.status], include: p.include, exclude: p.exclude,
    messages: p.messages, createdBy: p.createdBy, created: p.created ? toIsoDate(p.created) : ''
  }
}

export const gatePredicateType = 'https://relic.build/promotion-gate/v1'

export function releaseEvidenceView(e: Evidence): ReleaseEvidenceView {
  const view: ReleaseEvidenceView = {
    id: e.id, predicateType: e.predicateType, verified: e.verified, createdBy: e.createdBy,
    created: e.created ? toIsoDate(e.created) : ''
  }
  if (e.predicateType === gatePredicateType && e.predicate) {
    const d = JSON.parse(e.predicate) as { gate?: string, environment?: string, allowed?: boolean, error?: string }
    view.gate = { name: d.gate ?? '', environment: d.environment ?? '', allowed: !!d.allowed, error: d.error || undefined }
  }
  return view
}

const policyDecisionKinds: Record<PolicyDecisionKind, string> = {
  [PolicyDecisionKind.UNSPECIFIED]: 'Other',
  [PolicyDecisionKind.DOWNLOAD]: 'Download',
  [PolicyDecisionKind.BUILD]: 'Build',
  [PolicyDecisionKind.RELEASE]: 'Promotion'
}

export function policyDecisionView(d: PolicyDecision): PolicyDecisionView {
  return {
    kind: policyDecisionKinds[d.kind] ?? 'Other', subject: d.subject, policy: d.policy, watch: d.watch || undefined,
    rule: d.rule || undefined, reason: d.reason, actor: d.actor, count: toNumber(d.count),
    lastSeen: d.lastSeen ? toIsoDate(d.lastSeen) : undefined
  }
}
