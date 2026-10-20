import type { VNode } from 'vue'

// The docs name the server relic.example.com; a dashboard build shows the
// address it is served from instead.
const placeholder = 'relic.example.com'

export function useServerAddress() {
  const dashboard = useAppConfig().relic.dashboard
  const url = dashboard ? useRequestURL() : new URL(`https://${placeholder}`)

  function text(s: string): string {
    if (!dashboard) {
      return s
    }
    return s.replaceAll(`https://${placeholder}`, url.origin).replaceAll(placeholder, url.host)
  }

  return {
    origin: url.origin,
    text,
    vnodes: (nodes: VNode[]) => (dashboard ? replaceVNodeText(nodes, text) : nodes) as VNode[]
  }
}
