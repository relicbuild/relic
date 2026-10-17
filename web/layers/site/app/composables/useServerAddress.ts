import { cloneVNode, isVNode, type VNode, type VNodeArrayChildren } from 'vue'

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

  function children(nodes: VNodeArrayChildren): VNodeArrayChildren {
    return nodes.map((node) => {
      if (typeof node === 'string') {
        return text(node)
      }
      if (Array.isArray(node)) {
        return children(node)
      }
      if (!isVNode(node)) {
        return node
      }
      if (typeof node.children === 'string' || Array.isArray(node.children)) {
        const copy = cloneVNode(node)
        copy.children = typeof node.children === 'string' ? text(node.children) : children(node.children)
        return copy
      }
      return node
    })
  }

  return {
    origin: url.origin,
    text,
    vnodes: (nodes: VNode[]) => (dashboard ? children(nodes) : nodes) as VNode[]
  }
}
