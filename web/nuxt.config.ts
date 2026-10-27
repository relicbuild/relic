import { readdirSync, readFileSync } from 'node:fs'

const backend = (process.env.RELIC_BACKEND || 'https://localhost:8090').replace(/\/+$/, '')

// Not '/api' alone: it would also take /api-reference, a UI page, to
// the server; and a proxied prefix is cut at a '/', so '/api/relic.v1.' would
// not do either.
const genDir = new URL('./app/gen/relic/v1/', import.meta.url)
const relicServices = readdirSync(genDir)
  .filter(f => f.endsWith('_pb.ts'))
  .flatMap(f => [...readFileSync(new URL(f, genDir), 'utf8').matchAll(/@generated from service (relic\.v1\.\w+)/g)].map(m => m[1]))
const relicAPIPrefixes = ['/api/v1', ...relicServices.map(service => `/api/${service}`)]

// layers/site (the landing page, the docs and the API reference) is extended
// automatically; site/ builds it alone for relic.build.
export default defineNuxtConfig({
  modules: [
    '@nuxt/eslint'
  ],

  devtools: {
    enabled: true
  },

  nitro: {
    devProxy: Object.fromEntries(
      relicAPIPrefixes.map(prefix => [prefix, { target: backend + prefix, changeOrigin: true }])
    ),
    prerender: {
      // '/' would write index.html too, at the same time as Nuxt's SPA shell
      // '/index.html', and the two writes interleave into one broken file.
      ignore: ['/openapi.json', '/sitemap.md', '/raw/', '/.well-known/', route => route === '/']
    }
  },

  eslint: {
    config: {
      stylistic: {
        commaDangle: 'never',
        braceStyle: '1tbs'
      }
    }
  },

  // A self-hosted server has no address known at build time, and the
  // documents that list absolute links would all point at localhost.
  sitemap: {
    zeroPrerender: true
  }
})
