import { Container, getContainer } from '@cloudflare/containers'
import { relayedRequest, relicSettings } from './relay'

export class Relic extends Container<Env> {
  defaultPort = 8090
  pingEndpoint = 'relic/healthz'
  sleepAfter = '15m'

  constructor(ctx: DurableObjectState<object>, env: Env) {
    super(ctx, env)
    this.envVars = relicSettings(env)
  }
}

export default {
  async fetch(request, env): Promise<Response> {
    try {
      return await getContainer(env.RELIC).fetch(relayedRequest(request))
    } catch (error) {
      console.error('relay to the Relic container failed', error)
      return new Response('Relic is not reachable; try again shortly.\n', {
        status: 503,
        headers: { 'Retry-After': '10' }
      })
    }
  },

  // The cron trigger keeps the container awake, so the worker's background jobs keep running.
  async scheduled(_controller, env): Promise<void> {
    await getContainer(env.RELIC).fetch('http://relic/healthz')
  }
} satisfies ExportedHandler<Env>
