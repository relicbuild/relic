import { defineNuxtPlugin, injectHead } from '#imports'

// Scalar's component adds this inline script, which the server's script-src blocks; its own useColorMode sets the same body class.
const scalarColorModeScript = 'scalar-color-mode-script'

export default defineNuxtPlugin(() => {
  injectHead().use({
    key: 'relic:without-scalar-color-mode-script',
    hooks: {
      'tags:resolve': (context: { tags: { props: { id?: string } }[] }) => {
        context.tags = context.tags.filter(tag => tag.props.id !== scalarColorModeScript)
      }
    }
  })
})
