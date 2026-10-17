import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { FindingOccurrenceSchema, FindingSchema, Severity } from '~/gen/relic/v1/scan_pb'
import { PolicyDecisionKind, PolicyDecisionSchema } from '~/gen/relic/v1/policy_decision_pb'
import { findingView, globalFinding, policyDecisionView } from '~/utils/mappers'

describe('malicious packages', () => {
  it('are marked in both finding views', () => {
    const mal = { id: 'MAL-2025-1', severity: Severity.CRITICAL, packageName: 'evil-pkg', packageVersion: '1.0.0', malicious: true }
    expect(findingView(create(FindingSchema, mal)).malicious).toBe(true)
    expect(globalFinding(create(FindingOccurrenceSchema, mal)).malicious).toBe(true)
    expect(findingView(create(FindingSchema, { id: 'CVE-2021-1', severity: Severity.HIGH })).malicious).toBe(false)
  })
})

describe('policyDecisionView', () => {
  it('names the kind and turns the count and time into plain values', () => {
    const view = policyDecisionView(create(PolicyDecisionSchema, {
      kind: PolicyDecisionKind.BUILD, subject: 'app/42', policy: 'no-critical', watch: 'builds', rule: 'critical',
      reason: 'CVE-2021-44228 is critical', actor: 'ci', count: 3n, lastSeen: timestampFromDate(new Date('2026-10-14T12:00:00Z'))
    }))
    expect(view).toEqual({
      kind: 'Build', subject: 'app/42', policy: 'no-critical', watch: 'builds', rule: 'critical',
      reason: 'CVE-2021-44228 is critical', actor: 'ci', count: 3, lastSeen: '2026-10-14T12:00:00.000Z'
    })
  })

  it('leaves out an empty watch, rule and time', () => {
    const view = policyDecisionView(create(PolicyDecisionSchema, { kind: PolicyDecisionKind.DOWNLOAD, subject: 'npm/left-pad', count: 1n }))
    expect(view.kind).toBe('Download')
    expect(view.watch).toBeUndefined()
    expect(view.rule).toBeUndefined()
    expect(view.lastSeen).toBeUndefined()
  })
})
