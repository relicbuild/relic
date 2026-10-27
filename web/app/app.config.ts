export default defineAppConfig({
  relic: {
    dashboard: true
  },
  ui: {
    dashboardNavbar: {
      slots: {
        title: 'max-sm:min-w-min max-sm:line-clamp-2 max-sm:whitespace-normal max-sm:wrap-break-word max-sm:text-sm'
      }
    },
    pageCard: {
      slots: {
        container: 'min-w-0'
      }
    }
  }
})
