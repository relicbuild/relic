import { describe, expect, it } from 'vitest'
import { buildRunLink, buildRunsLink, buildsLink } from '~/utils/build-links'

describe('build links', () => {
  it.each([
    { name: 'builds outside projects', link: buildsLink(), want: { path: '/builds', query: {} } },
    { name: 'builds of a project', link: buildsLink('shop'), want: { path: '/builds', query: { project: 'shop' } } },
    { name: 'runs of a build outside projects', link: buildRunsLink('app/web'), want: { path: '/builds/app%2Fweb', query: {} } },
    { name: 'runs of a project build', link: buildRunsLink('app', 'shop'), want: { path: '/builds/app', query: { project: 'shop' } } },
    { name: 'a run of a project build', link: buildRunLink('app', '7 rc', 'shop'), want: { path: '/builds/app/7%20rc', query: { project: 'shop' } } }
  ])('$name', ({ link, want }) => {
    expect(link).toEqual(want)
  })
})
