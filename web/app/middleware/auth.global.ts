export default defineNuxtRouteMiddleware(async (to) => {
  const open = to.path === '/' || to.path === '/login' || to.path === '/api-reference' || to.path === '/docs' || to.path.startsWith('/docs/')
  const { me, refresh } = useSession()
  let principal = me.value
  try {
    principal ??= await refresh()
  } catch (err) {
    if (open) {
      return
    }
    return abortNavigation(createError({ statusCode: 503, statusMessage: `Could not reach the server: ${relicErrorMessage(err, 'no response')}` }))
  }
  if (principal || open) {
    return
  }
  return navigateTo({ path: '/login', query: { redirect: to.fullPath } })
})
