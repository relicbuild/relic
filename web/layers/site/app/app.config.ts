export default defineAppConfig({
  // true in the build the server embeds, which adds the dashboard.
  relic: {
    dashboard: false
  },
  ui: {
    colors: {
      primary: 'green',
      neutral: 'slate'
    },
    select: {
      slots: {
        placeholder: 'truncate text-muted'
      }
    },
    selectMenu: {
      slots: {
        placeholder: 'truncate text-muted'
      }
    }
  }
})
