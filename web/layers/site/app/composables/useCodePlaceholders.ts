import type { VNode } from 'vue'

// The docs' code names Relic's release vX.Y.Z (a chart version X.Y.Z); a build
// that knows the release, from NUXT_PUBLIC_RELIC_VERSION, shows it instead.
const versionPlaceholder = 'X.Y.Z'

export function useCodePlaceholders() {
  const server = useServerAddress()
  const version = String(useRuntimeConfig().public.relicVersion ?? '')
  const release = /^v\d+\.\d+\.\d+/.test(version) ? version.slice(1) : ''

  function text(s: string): string {
    const addressed = server.text(s)
    return release ? addressed.replaceAll(versionPlaceholder, release) : addressed
  }

  return {
    text,
    vnodes: (nodes: VNode[]) => replaceVNodeText(nodes, text) as VNode[]
  }
}
