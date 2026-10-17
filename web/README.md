# Relic web UI

Nuxt 4 + Nuxt UI v4 single-page app, scaffolded from the
[Nuxt UI dashboard template](https://github.com/nuxt-ui-templates/dashboard)
(MIT, see `LICENSE.nuxt-ui-template`).

```bash
pnpm install
pnpm dev          # proxies API prefixes to the Go server (RELIC_BACKEND, default https://localhost:8090; trust its CA with NODE_EXTRA_CA_CERTS)
pnpm generate     # static build in .output/public, embedded by the image build
pnpm lint && pnpm typecheck
```
