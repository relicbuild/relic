import { describe, expect, it, vi } from 'vitest'
import { findFolderNode, openExpandedFolders, type FolderNode } from '~/utils/folder-tree'

function tree(): FolderNode[] {
  return [
    { path: 'org', folder: true, children: [{ path: 'org/app', folder: true }] },
    { path: 'readme.txt', folder: false }
  ]
}

describe('findFolderNode', () => {
  it('finds a nested node and nothing outside the tree', () => {
    const roots = tree()
    expect(findFolderNode(roots, 'org/app')?.path).toBe('org/app')
    expect(findFolderNode(roots, 'other')).toBeUndefined()
  })
})

describe('openExpandedFolders', () => {
  it('loads every expanded folder once', async () => {
    const roots = tree()
    const load = vi.fn(async (path: string) => [{ path: `${path}/x`, folder: false }])

    const opened = await openExpandedFolders(roots, ['org'], load)

    expect(load).toHaveBeenCalledTimes(1)
    expect(opened.expanded).toEqual(['org'])
    expect(findFolderNode(roots, 'org/x')).toBeDefined()
    expect(await openExpandedFolders(roots, ['org'], load)).toEqual({ expanded: ['org'], failures: [] })
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('closes a folder that does not load, and the folders under it', async () => {
    const roots = tree()
    const refused = new Error('permission denied')
    const load = vi.fn(async () => {
      throw refused
    })

    const opened = await openExpandedFolders(roots, ['org', 'org/app'], load)

    expect(opened.expanded).toEqual([])
    expect(opened.failures).toEqual([{ path: 'org', error: refused }])
    expect(findFolderNode(roots, 'org')?.loaded).toBeFalsy()
  })
})
