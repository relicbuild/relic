// releaseTag returns a build's version when it names a release (vX.Y.Z, with an optional suffix), else ''.
export function releaseTag(version: string): string {
  return /^v\d+\.\d+\.\d+/.test(version) ? version : ''
}
