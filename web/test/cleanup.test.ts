import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { CleanupPolicySchema } from '~/gen/relic/v1/cleanup_pb'
import { cleanupCriteria, cleanupPolicyView } from '~/utils/mappers'

describe('cleanupCriteria', () => {
  it.each([
    [{ olderThanDays: 30, notDownloadedDays: 0, keepLatest: 5 }, 'older than 30 days · keep 5 latest'],
    [{ olderThanDays: 0, notDownloadedDays: 1, keepLatest: 0 }, 'not downloaded for 1 day'],
    [{ olderThanDays: 7, notDownloadedDays: 14, keepLatest: 2 }, 'older than 7 days · not downloaded for 14 days · keep 2 latest'],
    [{ olderThanDays: 0, notDownloadedDays: 0, keepLatest: 0 }, '']
  ])('summarizes %o', (policy, want) => {
    expect(cleanupCriteria(policy)).toBe(want)
  })
})

describe('cleanupPolicyView', () => {
  it('turns the moved count into a number and an empty error into none', () => {
    const view = cleanupPolicyView(create(CleanupPolicySchema, { name: 'old', lastMoved: 3n, lastError: '' }))
    expect(view.lastMoved).toBe(3)
    expect(view.lastError).toBeUndefined()
    expect(view.lastRun).toBeUndefined()
  })

  it('keeps the last run and its error', () => {
    const view = cleanupPolicyView(create(CleanupPolicySchema, { name: 'old', lastRun: timestampFromDate(new Date('2026-01-02T03:04:05Z')), lastError: 'storage unavailable' }))
    expect(view.lastRun).toBe('2026-01-02T03:04:05.000Z')
    expect(view.lastError).toBe('storage unavailable')
  })
})
