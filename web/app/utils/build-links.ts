import type { RouteLocationRaw } from 'vue-router'

const projectQuery = (project: string) => (project ? { project } : {})

// The builds page, showing the builds of a project, or those outside projects.
export function buildsLink(project = ''): RouteLocationRaw {
  return { path: '/builds', query: projectQuery(project) }
}

// The page listing the runs of a build.
export function buildRunsLink(name: string, project = ''): RouteLocationRaw {
  return { path: `/builds/${encodeURIComponent(name)}`, query: projectQuery(project) }
}

// The page of one build run.
export function buildRunLink(name: string, number: string, project = ''): RouteLocationRaw {
  return { path: `/builds/${encodeURIComponent(name)}/${encodeURIComponent(number)}`, query: projectQuery(project) }
}
