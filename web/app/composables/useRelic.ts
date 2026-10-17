import { Code, ConnectError, createClient, type Interceptor, type Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt'
import { ArtifactService } from '~/gen/relic/v1/artifact_pb'
import { BuildService } from '~/gen/relic/v1/build_pb'
import { CleanupService } from '~/gen/relic/v1/cleanup_pb'
import { EvidenceService } from '~/gen/relic/v1/evidence_pb'
import { ReleaseService } from '~/gen/relic/v1/release_pb'
import { AuditService } from '~/gen/relic/v1/audit_pb'
import { IdentityProviderService } from '~/gen/relic/v1/identity_provider_pb'
import { WorkloadIdentityService } from '~/gen/relic/v1/workload_identity_pb'
import { QuarantineService } from '~/gen/relic/v1/quarantine_pb'
import { WaiverService } from '~/gen/relic/v1/waiver_pb'
import { PolicyDecisionService } from '~/gen/relic/v1/policy_decision_pb'
import { PromotionGateService } from '~/gen/relic/v1/promotion_gate_pb'
import { WebhookService } from '~/gen/relic/v1/webhook_pb'
import { ProjectService } from '~/gen/relic/v1/project_pb'
import { AgentRegistryService } from '~/gen/relic/v1/agent_registry_pb'
import { LdapService } from '~/gen/relic/v1/ldap_pb'
import { PackageService } from '~/gen/relic/v1/package_pb'
import { ReplicationService } from '~/gen/relic/v1/replication_pb'
import { Format, RepositoryClass, RepositoryService } from '~/gen/relic/v1/repository_pb'
import { ScanService } from '~/gen/relic/v1/scan_pb'
import { SecurityService } from '~/gen/relic/v1/security_pb'
import { SessionService } from '~/gen/relic/v1/session_pb'
import { SystemService } from '~/gen/relic/v1/system_pb'
import { TrashService } from '~/gen/relic/v1/trash_pb'

const csrf: Interceptor = next => async (req) => {
  req.header.set('X-Relic-UI', '1')
  return await next(req)
}

const expired: Interceptor = next => async (req) => {
  try {
    return await next(req)
  } catch (err) {
    if (ConnectError.from(err).code === Code.Unauthenticated && req.service.typeName !== SessionService.typeName) {
      useState('session:me').value = null
      const route = useRouter().currentRoute.value
      if (route.path !== '/login') {
        await navigateTo({ path: '/login', query: { redirect: route.fullPath } })
      }
    }
    throw err
  }
}

let transport: Transport | undefined

function clients(t: Transport) {
  return {
    artifacts: createClient(ArtifactService, t),
    builds: createClient(BuildService, t),
    cleanup: createClient(CleanupService, t),
    evidence: createClient(EvidenceService, t),
    releases: createClient(ReleaseService, t),
    audit: createClient(AuditService, t),
    identityProviders: createClient(IdentityProviderService, t),
    workloadIdentities: createClient(WorkloadIdentityService, t),
    quarantine: createClient(QuarantineService, t),
    waivers: createClient(WaiverService, t),
    policyDecisions: createClient(PolicyDecisionService, t),
    promotionGates: createClient(PromotionGateService, t),
    webhooks: createClient(WebhookService, t),
    projects: createClient(ProjectService, t),
    agentRegistry: createClient(AgentRegistryService, t),
    ldap: createClient(LdapService, t),
    packages: createClient(PackageService, t),
    replications: createClient(ReplicationService, t),
    repositories: createClient(RepositoryService, t),
    scans: createClient(ScanService, t),
    security: createClient(SecurityService, t),
    sessions: createClient(SessionService, t),
    system: createClient(SystemService, t),
    trash: createClient(TrashService, t)
  }
}

let cached: ReturnType<typeof clients> | undefined

// useRelic returns the Relic API's clients, one per service.
export function useRelic() {
  transport ??= createConnectTransport({ baseUrl: '/api', interceptors: [csrf, expired] })
  cached ??= clients(transport)
  return cached
}

// connectErrorOf is the ConnectError behind error.
function connectErrorOf(error: unknown): ConnectError {
  const cause = (error as { cause?: unknown } | null)?.cause
  return ConnectError.from(cause instanceof ConnectError ? cause : error)
}

// relicErrorMessage is a failed call's message, or fallback.
export function relicErrorMessage(error: unknown, fallback: string): string {
  return connectErrorOf(error).rawMessage || fallback
}

// isNotFound reports whether a call failed because its subject does not exist.
export function isNotFound(error: unknown): boolean {
  return connectErrorOf(error).code === Code.NotFound
}

// iso renders a timestamp as the ISO string the format helpers take.
export function toIsoDate(ts?: Timestamp): string {
  return ts ? timestampDate(ts).toISOString() : ''
}

// num turns a 64-bit integer (a bigint in generated messages) into a number.
export function toNumber(v?: bigint | number): number {
  return Number(v ?? 0)
}

// filePath is a path under /api/v1/<kind>/<repository>/<path>, escaped.
export function filePath(kind: 'files' | 'archive' | 'sbom', repo: string, path: string): string {
  return `/api/v1/${kind}/${[repo, ...path.split('/').filter(Boolean)].map(encodeURIComponent).join('/')}`
}

// uploadFile PUTs a file to /api/v1/files/<repo>/<path> with the session
// cookie, reporting progress (0 to 1).
export function uploadFile(repo: string, path: string, file: File, onProgress?: (fraction: number) => void, signal?: AbortSignal): Promise<void> {
  return putFile(filePath('files', repo, path), file, onProgress, signal)
}

// importVulnerabilityDatabase uploads a vulnerability database archive for
// the worker to install (administrators).
export function importVulnerabilityDatabase(file: File, onProgress?: (fraction: number) => void): Promise<void> {
  return putFile('/api/v1/vulnerability-database', file, onProgress)
}

function putFile(url: string, file: File, onProgress?: (fraction: number) => void, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('PUT', url)
    xhr.setRequestHeader('X-Relic-UI', '1')
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) {
        onProgress?.(e.loaded / e.total)
      }
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        onProgress?.(1)
        return resolve()
      }
      let message = `${xhr.status} ${xhr.statusText}`
      try {
        message = (JSON.parse(xhr.responseText) as { message?: string }).message || message
      } catch {
        // not JSON: keep the status
      }
      reject(new Error(message))
    }
    xhr.onerror = () => reject(new Error('The upload failed: the server could not be reached.'))
    xhr.onabort = () => reject(new DOMException('The upload was cancelled.', 'AbortError'))
    signal?.addEventListener('abort', () => xhr.abort(), { once: true })
    xhr.send(file)
  })
}

// repositoryClassName names a repository class the way the UI shows it.
export function repositoryClassName(c: RepositoryClass): 'local' | 'remote' | 'virtual' | '' {
  switch (c) {
    case RepositoryClass.LOCAL: return 'local'
    case RepositoryClass.REMOTE: return 'remote'
    case RepositoryClass.VIRTUAL: return 'virtual'
    default: return ''
  }
}

// repositoryClassOf is the RepositoryClass for a class name.
export function repositoryClassOf(name: string): RepositoryClass {
  return ({ local: RepositoryClass.LOCAL, remote: RepositoryClass.REMOTE, virtual: RepositoryClass.VIRTUAL } as Record<string, RepositoryClass>)[name] ?? RepositoryClass.UNSPECIFIED
}

// enumName names an enum value the way Relic does: the value's name,
// lowercase, with no separators (FORMAT_HELM_OCI is helmoci); '' for the
// zero value.
export function enumName(e: Record<number, string>, v: number): string {
  return v === 0 ? '' : (e[v] ?? '').toLowerCase().replaceAll('_', '')
}

// enumValue is the value of enum e named name (enumName's inverse); 0 when
// there is none.
export function enumValue(e: Record<string, string | number>, name: string): number {
  for (const v of Object.values(e)) {
    if (typeof v === 'number' && enumName(e as unknown as Record<number, string>, v) === name) {
      return v
    }
  }
  return 0
}

// formatName names a format the way Relic does; '' when unspecified.
export function formatName(f: Format): string {
  return enumName(Format, f)
}

// repositoryFormatOf is the Format for a format name.
export function repositoryFormatOf(name: string): Format {
  return enumValue(Format, name) as Format
}

// exists reports whether get finds its subject (false on NotFound).
export async function exists(get: () => Promise<unknown>): Promise<boolean> {
  try {
    await get()
    return true
  } catch (err) {
    if (isNotFound(err)) {
      return false
    }
    throw err
  }
}
