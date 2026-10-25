import { describe, expect, it, vi } from 'vitest'
import { apiReferenceConfiguration } from '~~/layers/site/app/utils/api-reference'

describe('apiReferenceConfiguration', () => {
  it('loads no fonts from Scalar and sets no deprecated option', () => {
    const configuration = apiReferenceConfiguration('openapi: 3.1.0', true)

    expect(configuration.withDefaultFonts).toBe(false)
    expect(configuration).not.toHaveProperty('showToolbar')
    expect(configuration.content).toBe('openapi: 3.1.0')
    expect(configuration.darkMode).toBe(true)
  })
})

describe('the API reference head', () => {
  it('drops the inline colour-mode script Scalar adds and keeps other tags', async () => {
    useNuxtApp().runWithContext(() => useHead({
      script: [
        { id: 'scalar-color-mode-script', innerHTML: 'document.body.classList.add("dark-mode")' },
        { id: 'kept', src: '/kept.js' }
      ]
    }))

    await vi.waitFor(() => expect(document.getElementById('kept')).not.toBeNull())

    expect(document.getElementById('scalar-color-mode-script')).toBeNull()
    expect(document.getElementById('kept')).not.toBeNull()
  })
})
