import { fileURLToPath } from 'node:url'

export default defineNuxtConfig({
  extends: ['docus'],

  modules: [
    '@nuxt/ui',
    '@scalar/nuxt'
  ],

  ssr: false,

  app: {
    baseURL: '/'
  },

  css: [fileURLToPath(new URL('./app/assets/css/main.css', import.meta.url))],

  colorMode: {
    preference: 'dark',
    fallback: 'dark'
  },

  content: {
    build: {
      markdown: {
        highlight: {
          theme: {
            default: 'github-light-default',
            dark: 'github-dark-default'
          },
          langs: ['bash', 'go', 'groovy', 'hcl', 'ini', 'json', 'toml', 'ts', 'xml', 'yaml']
        }
      }
    }
  },

  ui: {
    fonts: false
  },

  routeRules: {
    '/docs': { redirect: '/docs/overview/introduction' }
  },

  compatibilityDate: '2026-06-30',

  // Docus's own fix for @vercel/oidc's CommonJS export (a dependency of its AI assistant).
  vite: {
    optimizeDeps: {
      include: ['docus > @vercel/oidc']
    }
  },

  hooks: {
    // Docus adds its own landing page when the app root has no pages/index.vue;
    // this layer's is the landing page.
    'pages:resolved'(pages) {
      const docusLanding = pages.findIndex(page => page.path === '/' && page.file?.includes('docus/app/templates/landing.vue'))
      if (docusLanding !== -1) {
        pages.splice(docusLanding, 1)
      }
    }
  },

  // Air-gap friendly: no runtime icon CDN and no remote font downloads.
  icon: {
    provider: 'none',
    clientBundle: {
      scan: {
        globInclude: ['**/app/**/*.{vue,ts}', '**/content/**/*.md', '**/content/**/.navigation.yml'],
        // Not the default '.*': the docs folders' icons are in .navigation.yml.
        globExclude: ['node_modules', '.nuxt', '.output']
      }
    }
  },

  ogImage: { enabled: false },

  // The module's own page is parked on an unused path.
  scalar: {
    pathRouting: { basePath: '/_scalar' }
  }
})
