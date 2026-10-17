export interface FolderNode {
  path: string
  folder: boolean
  loaded?: boolean
  children?: FolderNode[]
}

export function findFolderNode<T extends FolderNode>(nodes: T[] | undefined, path: string): T | undefined {
  for (const n of nodes ?? []) {
    if (n.path === path) {
      return n
    }
    if (path.startsWith(`${n.path}/`)) {
      return findFolderNode(n.children as T[] | undefined, path)
    }
  }
}

export interface FolderFailure {
  path: string
  error: unknown
}

export async function openExpandedFolders<T extends FolderNode>(
  roots: T[] | undefined,
  expanded: string[],
  load: (path: string) => Promise<T[]>
): Promise<{ expanded: string[], failures: FolderFailure[] }> {
  const failures: FolderFailure[] = []

  for (const path of expanded) {
    if (failures.some(f => path.startsWith(`${f.path}/`))) {
      continue
    }
    const node = findFolderNode(roots, path)
    if (!node?.folder || node.loaded) {
      continue
    }
    node.loaded = true
    try {
      node.children = await load(node.path)
    } catch (error) {
      node.loaded = false
      failures.push({ path, error })
    }
  }

  return {
    expanded: expanded.filter(p => !failures.some(f => p === f.path || p.startsWith(`${f.path}/`))),
    failures
  }
}
