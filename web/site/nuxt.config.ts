import { copyFile } from 'node:fs/promises'
import { join } from 'node:path'

export default defineNuxtConfig({
  extends: ['../layers/site'],

  ssr: true,

  nitro: {
    prerender: {
      crawlLinks: true,
      routes: ['/', '/api-reference'],
      ignore: ['/openapi.json'],
      failOnError: true
    }
  },

  hooks: {
    async 'nitro:build:public-assets'(nitro) {
      await copyFile(
        new URL('../../api/apispec/relic.openapi.json', import.meta.url),
        join(nitro.options.output.publicDir, 'openapi.json')
      )
    }
  }
})
