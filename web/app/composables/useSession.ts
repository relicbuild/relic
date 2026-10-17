import { Code, ConnectError } from '@connectrpc/connect'
import type { Principal } from '~/gen/relic/v1/session_pb'

// useSession holds the signed-in user, shared across the app.
export function useSession() {
  const me = useState<Principal | null>('session:me', () => null)
  const administers = computed(() => !!me.value?.admin)
  const views = computed(() => !!me.value?.admin || !!me.value?.viewer)
  const { sessions } = useRelic()

  async function refresh(): Promise<Principal | null> {
    try {
      me.value = (await sessions.getCurrentUser({})).principal ?? null
    } catch (err) {
      if (ConnectError.from(err).code !== Code.Unauthenticated) {
        throw err
      }
      me.value = null
    }
    return me.value
  }

  async function signIn(username: string, password: string): Promise<Principal> {
    const res = await sessions.login({ username, password })
    if (!res.principal) {
      throw new Error('sign-in returned no user')
    }
    me.value = res.principal
    return res.principal
  }

  async function signOut() {
    await sessions.logout({})
    me.value = null
  }

  return { me, administers, views, refresh, signIn, signOut }
}
