import { cloneVNode, isVNode, type VNodeArrayChildren } from 'vue'

// replaceVNodeText returns nodes with every text child passed through text.
export function replaceVNodeText(nodes: VNodeArrayChildren, text: (s: string) => string): VNodeArrayChildren {
  return nodes.map((node) => {
    if (typeof node === 'string') {
      return text(node)
    }
    if (Array.isArray(node)) {
      return replaceVNodeText(node, text)
    }
    if (!isVNode(node)) {
      return node
    }
    if (typeof node.children === 'string' || Array.isArray(node.children)) {
      const copy = cloneVNode(node)
      copy.children = typeof node.children === 'string' ? text(node.children) : replaceVNodeText(node.children, text)
      return copy
    }
    return node
  })
}
