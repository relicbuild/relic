import type { ComponentView, LicenseViolationView } from '~/utils/view-models'

// NO_LICENSE selects components without any license in a license filter.
export const NO_LICENSE = '\u0000none'

export interface ComponentFilters {
  search: string
  type: string
  licenses: string[]
  status: 'all' | 'violating' | 'compliant'
}

// licenseFacets lists the licenses the components name, most used first,
// each with how many components name it: "MIT OR Apache-2.0" counts for
// both.
export function licenseFacets(components: ComponentView[]): { label: string, value: string, count: number }[] {
  const counts = new Map<string, number>()
  let none = 0
  for (const c of components) {
    if (!c.licenseIds.length) none++
    for (const id of c.licenseIds) counts.set(id, (counts.get(id) ?? 0) + 1)
  }
  const facets = [...counts]
    .sort(([a, x], [b, y]) => y - x || a.localeCompare(b))
    .map(([id, count]) => ({ label: id, value: id, count }))
  return none ? [{ label: 'No license', value: NO_LICENSE, count: none }, ...facets] : facets
}

export function filterComponents(components: ComponentView[], f: ComponentFilters): ComponentView[] {
  const q = f.search.trim().toLowerCase()
  const picked = new Set(f.licenses.map(l => l.toLowerCase()))
  return components.filter(c =>
    (f.type === 'All' || c.type === f.type)
    && (!q || c.name.toLowerCase().includes(q) || c.licenses.some(l => l.toLowerCase().includes(q)))
    && (!picked.size
      || (picked.has(NO_LICENSE) && !c.licenseIds.length)
      || c.licenseIds.some(id => picked.has(id.toLowerCase())))
    && (f.status === 'all' || (f.status === 'violating') === (c.violations.length > 0))
  )
}

// licenseBadges lists a component's licenses, each with the violations of
// that license; violations of a component without a license come under
// "No license".
export function licenseBadges(c: ComponentView): { license: string, violations: LicenseViolationView[] }[] {
  const badges = c.licenses.map(license => ({ license, violations: c.violations.filter(v => v.license === license) }))
  const unnamed = c.violations.filter(v => !c.licenses.includes(v.license))
  return unnamed.length ? [...badges, { license: 'No license', violations: unnamed }] : badges
}
