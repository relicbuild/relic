import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it, onTestFinished } from 'vitest'
import { installedPackages, renderNotices } from '../scripts/third-party-notices'

function writePackage(dir: string, manifest: object, files: Record<string, string> = {}) {
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'package.json'), JSON.stringify(manifest))
  for (const [name, text] of Object.entries(files)) {
    writeFileSync(join(dir, name), text)
  }
}

describe('installedPackages', () => {
  it('lists each installed package once, with its declared license and license texts', () => {
    const root = mkdtempSync(join(tmpdir(), 'notices-'))
    onTestFinished(() => rmSync(root, { recursive: true, force: true }))
    const store = join(root, '.pnpm')
    const left = join(store, 'left@1.0.0', 'node_modules', 'left')
    const scoped = join(store, '@scope+right@2.0.0', 'node_modules', '@scope', 'right')

    writePackage(left, { name: 'left', version: '1.0.0', license: 'MIT' }, { 'LICENSE': 'left text\n', 'README.md': 'readme' })
    writePackage(scoped, { name: '@scope/right', version: '2.0.0', license: { type: 'ISC' } })
    writePackage(join(store, 'left@1.0.0_peer', 'node_modules', 'left'), { name: 'left', version: '1.0.0', license: 'MIT' }, { LICENSE: 'left text\n' })
    symlinkSync(left, join(store, '@scope+right@2.0.0', 'node_modules', 'left'))

    expect(installedPackages(store)).toEqual([
      { name: '@scope/right', version: '2.0.0', license: 'ISC', texts: [] },
      { name: 'left', version: '1.0.0', license: 'MIT', texts: ['left text'] }
    ])
  })
})

describe('renderNotices', () => {
  const notices = renderNotices([
    { name: 'second', version: '2.0.0', license: 'MIT', texts: ['shared text'] },
    { name: 'bare', version: '0.1.0', license: 'ISC', texts: [] },
    { name: 'first', version: '1.0.0', license: 'MIT', texts: ['shared text'] }
  ])

  it('writes a license text once, under the packages it covers', () => {
    expect(notices.split('shared text')).toHaveLength(2)
    expect(notices.indexOf('  first 1.0.0')).toBeGreaterThan(-1)
    expect(notices.indexOf('  second 2.0.0')).toBeGreaterThan(notices.indexOf('  first 1.0.0'))
    expect(notices.indexOf('shared text')).toBeGreaterThan(notices.indexOf('  second 2.0.0'))
  })

  it('lists a package without a license file under the license its manifest declares', () => {
    expect(notices).toContain('no license file')
    expect(notices.indexOf('  bare 0.1.0 (ISC)')).toBeGreaterThan(notices.indexOf('no license file'))
  })
})
