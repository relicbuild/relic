// The request the container receives: Relic trusts the X-Forwarded-* headers
// (RELIC_TRUSTED_PROXIES) for its own address and the client's. Without
// Expect, Go never answers 100 Continue, a status the container proxy refuses.
export function relayedRequest(request: Request): Request {
  const url = new URL(request.url)
  const headers = new Headers(request.headers)
  headers.delete('Expect')
  headers.set('X-Forwarded-Proto', url.protocol.slice(0, -1))
  headers.set('X-Forwarded-Host', url.host)
  const client = request.headers.get('CF-Connecting-IP')
  if (client) {
    headers.set('X-Forwarded-For', client)
  }
  return new Request(request, { headers })
}

// Every RELIC_* variable and secret of the Worker becomes the container's environment.
export function relicSettings(env: object): Record<string, string> {
  return Object.fromEntries(
    Object.entries(env).filter((entry): entry is [string, string] => entry[0].startsWith('RELIC_') && typeof entry[1] === 'string')
  )
}
