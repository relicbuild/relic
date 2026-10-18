import { describe, expect, it } from 'vitest'
import type { ComponentView } from '~/utils/view-models'
import { NO_LICENSE, filterComponents, licenseBadges, licenseFacets } from '~/utils/components-filter'

const comp = (name: string, licenseIds: string[], violating = false, type = 'npm'): ComponentView => ({
  name, version: '1', type, licenses: licenseIds.length ? [licenseIds.join(' OR ')] : [], licenseIds, locations: [],
  violations: violating ? [{ license: licenseIds.join(' OR '), watch: 'w', policy: 'p', rule: 'r', failBuild: false, blockDownload: true }] : []
})
const comps = [comp('dual', ['MIT', 'Apache-2.0']), comp('mit', ['MIT']), comp('gpl', ['GPL-3.0-only'], true), comp('bare', [], false, 'go')]
const all = { search: '', type: 'All', licenses: [], status: 'all' as const }
const names = (f: Parameters<typeof filterComponents>[1]) => filterComponents(comps, f).map(c => c.name)

describe('licenseFacets', () => {
  it('counts each license an expression names, most used first, with components without one', () => {
    expect(licenseFacets(comps)).toEqual([
      { label: 'No license', value: NO_LICENSE, count: 1 },
      { label: 'MIT', value: 'MIT', count: 2 },
      { label: 'Apache-2.0', value: 'Apache-2.0', count: 1 },
      { label: 'GPL-3.0-only', value: 'GPL-3.0-only', count: 1 }
    ])
  })
})

describe('filterComponents', () => {
  it.each([
    { name: 'no filter', f: all, want: ['dual', 'mit', 'gpl', 'bare'] },
    { name: 'one license of an expression', f: { ...all, licenses: ['Apache-2.0'] }, want: ['dual'] },
    { name: 'licenses are any of, ignoring case', f: { ...all, licenses: ['mit', 'GPL-3.0-only'] }, want: ['dual', 'mit', 'gpl'] },
    { name: 'no license', f: { ...all, licenses: [NO_LICENSE] }, want: ['bare'] },
    { name: 'violating', f: { ...all, status: 'violating' as const }, want: ['gpl'] },
    { name: 'compliant', f: { ...all, status: 'compliant' as const }, want: ['dual', 'mit', 'bare'] },
    { name: 'search matches a license', f: { ...all, search: 'apache' }, want: ['dual'] },
    { name: 'search matches a name, ignoring case', f: { ...all, search: 'BAR' }, want: ['bare'] },
    { name: 'one type', f: { ...all, type: 'go' }, want: ['bare'] },
    { name: 'another type', f: { ...all, type: 'npm' }, want: ['dual', 'mit', 'gpl'] }
  ])('$name', ({ f, want }) => {
    expect(names(f)).toEqual(want)
  })
})

describe('licenseBadges', () => {
  const violation = (license: string) => ({ license, watch: 'w', policy: 'permissive-licenses', rule: 'r', failBuild: false, blockDownload: true })

  it.each([
    { name: 'licenses without violations', c: comp('mit', ['MIT']), want: [{ license: 'MIT', violations: [] }] },
    { name: 'a violated license', c: { ...comp('gpl', ['GPL-3.0-only']), violations: [violation('GPL-3.0-only')] }, want: [{ license: 'GPL-3.0-only', violations: [violation('GPL-3.0-only')] }] },
    { name: 'a component without a license that violates a policy', c: { ...comp('bare', []), violations: [violation('')] }, want: [{ license: 'No license', violations: [violation('')] }] },
    { name: 'a component without a license or violations', c: comp('bare', []), want: [] }
  ])('$name', ({ c, want }) => {
    expect(licenseBadges(c)).toEqual(want)
  })
})
