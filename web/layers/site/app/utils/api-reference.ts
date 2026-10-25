// apiReferenceConfiguration returns the Scalar configuration of the API reference page; it uses no Scalar service, font or toolbar.
export function apiReferenceConfiguration(spec: string, dark: boolean) {
  return {
    content: spec,
    darkMode: dark,
    hideDarkModeToggle: true,
    hideClientButton: true,
    withDefaultFonts: false,
    agent: { disabled: true },
    mcp: { disabled: true },
    showDeveloperTools: 'never' as const,
    metaData: { title: 'Relic API reference' }
  }
}
