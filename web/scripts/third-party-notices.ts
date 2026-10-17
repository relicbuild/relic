import { lstatSync, readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

export interface InstalledPackage {
  name: string
  version: string
  license: string
  texts: string[]
}

const licenseFileName = /^(?:licen[cs]e|copying|notice|unlicense)/i
const separator = '-'.repeat(80)

function declaredLicense(manifest: { license?: string | { type?: string }, licenses?: { type?: string }[] }): string {
  const declared = typeof manifest.license === 'object' ? manifest.license.type : manifest.license

  return declared ?? manifest.licenses?.map(license => license.type).join(' OR ') ?? 'not declared'
}

function installedPackage(dir: string): InstalledPackage {
  const manifest = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'))
  const texts = readdirSync(dir)
    .filter(file => licenseFileName.test(file) && lstatSync(join(dir, file)).isFile())
    .sort()
    .map(file => readFileSync(join(dir, file), 'utf8').trim())

  return { name: manifest.name, version: manifest.version, license: declaredLicense(manifest), texts }
}

export function installedPackages(store: string): InstalledPackage[] {
  const packages = new Map<string, InstalledPackage>()

  for (const entry of readdirSync(store)) {
    const modules = join(store, entry, 'node_modules')
    if (!lstatSync(join(store, entry)).isDirectory() || !lstatSync(modules, { throwIfNoEntry: false })?.isDirectory()) {
      continue
    }

    const dirs = readdirSync(modules).flatMap(name => name.startsWith('@')
      ? readdirSync(join(modules, name)).map(scoped => join(modules, name, scoped))
      : [join(modules, name)])

    for (const dir of dirs) {
      if (!lstatSync(dir).isDirectory() || !lstatSync(join(dir, 'package.json'), { throwIfNoEntry: false })?.isFile()) {
        continue
      }

      const found = installedPackage(dir)
      packages.set(`${found.name} ${found.version}`, found)
    }
  }

  return [...packages.keys()].sort().map(key => packages.get(key)!)
}

export function renderNotices(packages: InstalledPackage[]): string {
  const labelsByText = new Map<string, string[]>()
  const withoutFile: string[] = []

  for (const installed of packages) {
    const label = `${installed.name} ${installed.version}`
    if (installed.texts.length === 0) {
      withoutFile.push(`${label} (${installed.license})`)
    }

    for (const text of installed.texts) {
      labelsByText.set(text, [...(labelsByText.get(text) ?? []), label])
    }
  }

  const groups = [...labelsByText.entries()]
    .map(([text, labels]) => ({ text, labels: labels.sort() }))
    .sort((a, b) => a.labels[0]!.localeCompare(b.labels[0]!) || a.text.localeCompare(b.text))

  const title = 'Installed to build the web UI'
  const lines = [title, '='.repeat(title.length), '']

  for (const group of groups) {
    lines.push(...group.labels.map(label => `  ${label}`), '', group.text, '', separator, '')
  }

  if (withoutFile.length > 0) {
    lines.push(
      'These packages publish no license file; each is listed with the license its manifest declares:',
      '',
      ...withoutFile.sort().map(label => `  ${label}`),
      ''
    )
  }

  return lines.join('\n')
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.stdout.write(renderNotices(installedPackages(join(import.meta.dirname, '..', 'node_modules', '.pnpm'))))
}
