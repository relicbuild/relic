# AGENTS.md

A guide for coding agents working on Relic: where things live, how the pieces
fit, which tools to use and what must stay true. Read it before changing
anything.

## What Relic is

A self-hosted artifact repository and security platform in one binary. It
stores packages and images for every common package manager, keeps their
content in S3-compatible storage, Azure Blob Storage, Google Cloud Storage or a
local directory, and their metadata in PostgreSQL or SQLite. It scans what it
stores (Relic Scan), records builds and releases, and replicates between
servers.

People and scripts use it through its own API (the Relic API), the web UI and
the `relic` CLI, all three built on that API. Package managers use their own
protocols. Relic also serves the JFrog® APIs, so `jf` and the clients it
configures work unchanged ([JFrog compatibility](#jfrog-compatibility)).

## Repository map

```
cmd/relic/              the one binary: relic server start|worker|migrate and the relic CLI (cobra)
api/
  proto/relic/v1/       the Relic API in protobuf (also the schema of relic.build/v1 YAML)
  gen/relic/v1/         generated Go; committed, never edited
  apispec/              the Relic API's OpenAPI description (generated with the Go code)
  relicapi/             Connect handlers of the Relic API: proto <-> domain services
  packages/             the package managers' own protocols (npm, PyPI, Maven, Go, NuGet, the
                        Docker registry, …), mounted at their paths
  scim/                 SCIM 2.0 provisioning of single sign-on users and groups
  mcpserver/            the read-only MCP tools relic mcp serves
  adapters/artifactory/ the JFrog-compatible APIs and their compatibility suite (compat/);
                        see JFrog compatibility
internal/               the domain, in modules:
  platform/             infrastructure without business rules: database (sqlc), objectstore,
                        localdisk, telemetry, sealing (secrets at rest), servertls, events,
                        httperror, httpserver (router, middleware), scripttest, terminal
  access/               who may do what: auth, security (users, groups, permissions), ldap, sso,
                        workload (workload identities), project, audit
  artifacts/            repositories and their content: repository, formats/<format>, itemquery,
                        archive, signingkey, cleanup, usage
  scanning/             scan (scanners, policies, blocking), cvecatalog, endoflife, malicious
  delivery/             buildinfo, release, evidence, replication, webhook
  systemstatus/         health, storage and background job status
  app/                  wiring (app.Run): config, backgroundjobs (River), openapi, ui (embedded web build)
queries/                SQL: postgresql/ for sqlc, sqlite/ for tools/sqlitebridge
migrations/             goose schema migrations, embedded: postgresql/ and sqlite/
web/                    Nuxt + Nuxt UI single-page app, embedded in the binary and served at /
  app/                  the dashboard: gen/ (generated Connect-ES client), composables/ (useRelic,
                        useSession), utils/ (view models, mappers), components/
  layers/site/          everything that works without a server: landing page, docs
                        (content/docs), API reference, install.sh
  site/                 relic.build: layers/site alone, prerendered (pnpm generate:site) and served
                        by a Cloudflare Worker (wrangler.jsonc) that Workers Builds deploys from
                        main when web/ changes
deploy/                 Compose stacks (docker-compose.yml with PostgreSQL and SeaweedFS and an
                        observability profile; docker-compose.single-node.yml), cloudflare/,
                        observability/ (dashboards, alert rules and their tests)
helm/relic/             the Helm chart; PostgreSQL (CloudNativePG), SeaweedFS and the monitoring
                        stack come from their official charts (Chart.lock)
test/                   e2e/ (every e2e suite, one Go package), compose/, helm/ (render tests),
                        install/ (install.sh), postgresql.sh and database-packages.sh
tools/                  protogen (own module), dashgen, metricsdoc, sqlitebridge, thirdpartynotices
.opencodereview/        the code reviewer's project rules
```

## Architecture

- **One binary** serves the APIs, the embedded UI and the worker.
- **The Relic API** is ConnectRPC under `/api/`, with REST transcoding under
  `/api/v1/`. Every method declares `option (relic.v1.access) = PUBLIC |
  AUTHENTICATED | VIEWER | ADMIN`; an interceptor enforces it and fails closed
  (`TestEveryMethodDeclaresAccess`). Errors are Connect codes. File contents
  never travel in messages: they are plain HTTP at `/api/v1/files/{repo}/{path}`
  and `/api/v1/archive/…`.
- **Layering**, enforced by depguard in `.golangci.yml`:
  - `api/**` never imports `internal/platform/database`; it uses domain
    services. `api/packages` never imports `api/adapters` or `api/relicapi`.
  - `internal/**` (except `internal/app` and tests) never imports `relic/api`.
  - Modules import downwards only: `platform` ← `access` ← `artifacts` ←
    `scanning` ← `delivery` ← `app`; `systemstatus` uses `platform` alone.
    `platform/httpserver` may use `access/auth`; no other `platform` package
    knows a module. A new package goes into the module whose data it owns; a
    new module gets its place in the order and its depguard rule in the same
    change.
- **Storage:** PostgreSQL through pgx and sqlc, or SQLite for a single node;
  goose migrations. Only `internal/platform/database` knows the engine.
  Content is stored once per SHA-256 in object storage.
- **Background work** runs on River; scanners run as subprocesses.
- **Auth:** Basic, bearer tokens and the UI's session cookie. The cookie is
  honoured under `/api/` only, and writes made with it need the `X-Relic-UI`
  header.

## Tools and versions

The Go version is the one in `go.mod`. Every other tool and library is on its
latest release that supports that Go version; when Go moves, the tools move
with it. A tool built with an older Go can fail on newer code (golangci-lint
cannot read packages compiled by a newer Go), so a failure that also happens on
unchanged code points to an outdated tool first.

| Area | Tools | Versions set in |
| --- | --- | --- |
| Go | pgx, sqlc, goose, River, connect-go, cobra, testcontainers-go, `go tool unfold` | `go.mod` (tool directives too) |
| Protobuf | buf, protoc-gen-go, protoc-gen-connect-go; protoc-gen-es for the web | `tools/protogen/go.mod`, `web/package.json` |
| Web | Nuxt, Nuxt UI, pnpm, connect-web, protobuf-es, zod | `web/package.json` |
| Lint and CI | golangci-lint, govulncheck, actions pinned by digest | `.github/workflows/ci.yml` |
| Chart | the CloudNativePG, SeaweedFS, kube-prometheus-stack and Grafana charts; Helm | `helm/relic/Chart.yaml` and `Chart.lock`; Helm in `.github/workflows/release.yml` |
| JFrog clients | `jf`, jfrog-client-go | `api/adapters/artifactory/compat/jf.sh`, `compat/sdk/go.mod` |

Bump a tool in every place that sets it, in one change.

## Generated code

Never edit generated files; change their source, regenerate and commit. Drift
tests fail when they are stale.

| Change | Regenerate with | Guarded by |
| --- | --- | --- |
| `api/proto/**` | `go tool -modfile=tools/protogen/go.mod buf generate`, then `cd web && pnpm gen:api` | `TestProtoUpToDate`, CI `web:check` |
| `queries/postgresql/*.sql` | `sqlc generate`, then `go run ./tools/sqlitebridge` | review |
| `queries/sqlite/*.sql` (one per PostgreSQL query, same name and kind) | `go run ./tools/sqlitebridge` | `TestSQLiteBridgeUpToDate`, `TestSQLiteQueriesPrepare` |
| HTTP routes | `go run ./api/adapters/artifactory/compat/routes` | `TestOpenAPIUpToDate` |
| jf commands | `go run ./api/adapters/artifactory/compat/inventory generate -cli "$(api/adapters/artifactory/compat/jf.sh)"` | CI `inventory:check` |
| Metrics (the metrics reference page) | `go run ./tools/metricsdoc` | `TestMetricsReferenceUpToDate` |
| Dashboards and alert rules (`tools/dashgen`) | `go run ./tools/dashgen` | `TestDashboardsUpToDate`, CI `observability:rules` |
| Chart dependencies (`helm/relic/Chart.yaml`) | `helm dependency update helm/relic`, with the four repositories `test/helm/chart_test.go` adds; a new CloudNativePG also refreshes `helm/relic/crds/cloudnative-pg.yaml` | `go test ./test/helm/` |
| jfrog-client-go round-trip documents (`api/adapters/artifactory/*/testdata/sdk`) | `cd api/adapters/artifactory/compat/sdk && go run ./payloads` | `TestPayloadsUpToDate` |

## Standards

### Naming

Names say what a thing is without their surroundings:
`RELIC_OBJECT_STORAGE_ENDPOINT`, not `RELIC_S3_ENDPOINT`; `GetBuildRun`, not
`GetRun`; `platform/objectstore`, not `platform/blob`. This holds for Helm
values, environment variables, packages, exported identifiers, protos, routes
and web files. Relic has no production users yet: rename outright, without
aliases.

Outside projects keep their own names: a chart dependency's values key is its
chart name (`cloudnative-pg`, `cluster`, `seaweedfs`), with no `alias`,
`nameOverride` or `fullnameOverride`.

- A noun phrase for what a function returns, a plain verb for what it does:
  `domainRepository`, `parseActions`, `runHealthCheck`.
- No narrative connectors: `repositoryOf` → `domainRepository`, `itemFromRow` →
  `domainItem`. `By` for a lookup key (`FindBySHA256`) and Go idioms
  (`FromContext`, `withTx`) stay.
- Converters: domain → proto `xMessage`, proto or row → domain `domainX`,
  domain → an adapter's wire type `wireX`, text → value `parseX`.
- No vague words (`helper`, `util`, `manager`, `data`, `info`, `misc`,
  `common`, `tmp`), filler verbs (`do`, `handle`, `process`, `ensure`, `maybe`)
  or decorative ones (`safe`, `smart`, `simple`, `better`), unless a library
  or format defines them. Say what the code guarantees: `safeJoin` →
  `joinWithin`.
- No abbreviations or one- and two-letter names beyond loop variables and
  receivers.
- Files are named after what they hold (`inventory.go`, not `model.go`);
  framework conventions stay (`utils/`, `_helpers.tpl`, `main.go`).
- Tests named after a function follow its renames; tests named as sentences
  stay. Generated code is renamed only at its source.
- Check a name is free in its scope before using it; rename with whole-word
  matches and review every string literal the rename touched.

### Go

- Load the Go skills through `golang-how-to` and follow them.
- Tests first: every change gets a test that fails without it. Table-driven,
  named cases, `t.Parallel()` where possible. Integration tests carry
  `//go:build integration` and use `databasetest.NewDB`; server-level ones live
  in `internal/app` with `server(t)` and `serverDB(t)`.
- Formatting is golangci-lint's (gofumpt, goimports, golines at 120 columns,
  wsl_v5): `golangci-lint fmt ./...`, then `golangci-lint run --fix ./...`.
  When golines splits a statement with a trailing `//nolint`, move the
  directive above the statement; when it splits an expression awkwardly,
  assign the value first.
- Nothing is inlined: each element of a composite literal, each field of a
  struct type and each statement of a function literal goes on its own line
  (`struct{}`, `T{}` and `func() {}` stay). `go tool unfold -changed -w`
  applies it to the files changed against `origin/main`; CI checks the files
  changed since the latest tag.
- Errors wrap with context (`%w`); domain errors map to Connect codes in
  `api/relicapi/access.go` (`codeOf`). Never leak internal errors to clients.
- Comments: a one-sentence doc comment on exported identifiers and packages,
  tool directives, `// PromQL:` notes, and constraints the code cannot
  express. No history, rationale or restating the code; the why goes in the
  commit message.

### Observability

- A feature is done when it is observable: new operations get a span
  (`telemetry.Tracer()`, `telemetry.End`) and bounded metrics in the same
  change.
- Declare metrics in `internal/platform/telemetry`; write the PromQL that uses
  a histogram in a `// PromQL:` comment above it.
- Label values pass through the label functions (`Family`, `Route`, `Method`,
  `Format`, `Class`, …), which turn anything unknown into `other`. Never a
  user, path, digest, token or address.
- Latency is a histogram observed with `observe` (the trace ID becomes an
  exemplar). Log with the `…Context` slog calls.
- Alert rules live in `deploy/observability/prometheus/relic-rules.yaml`, each
  with promtool tests and a runbook section in the Monitoring guide.

### SQL and migrations

- Migrations change the schema only; one-off data conversions never go into
  `migrations/`.
- A schema change adds the next number (one above the highest in
  `migrations/postgresql/`) to both `migrations/postgresql/` and
  `migrations/sqlite/`; `TestSchemasMatch` compares the schemas they build.
- Parameterize every query. A SQLite query matches its PostgreSQL one in name,
  kind, `@names` and column order; arrays arrive as JSON for `json_each`,
  times as fixed-width UTC text.
- Run the integration suite on both engines (`task test:integration`).

### Web

- Load the `nuxt-ui` skill and its references before any change in `web/`.
  Use Nuxt UI components and semantic colors (`text-muted`, `bg-elevated`, …),
  never raw palette classes.
- Call the backend through `useRelic()`; upload with `uploadFile`.
- Full-page tables: `UDashboardPanel :ui="fullBleedPanel"`, `<TableFilters>`,
  `UTable sticky :ui="denseTable"`, `<TablePagination>` (see
  `pages/security/index.vue`).
- API messages map to view models in `utils/mappers.ts`; pages stay thin.

### Documentation

- User docs live in `web/layers/site/content/docs`. Besides them the tree holds
  only this guide, READMEs that say how to run a part, and generated
  inventories: no design notes, plans, runbooks or decision records.
- Every claim in the docs is checked against the code before it is written.
  Plain sentences, no narrative or marketing.
- The docs name the server `https://relic.example.com` and Relic's release
  `vX.Y.Z` (a chart version `X.Y.Z`); builds fill in the real ones.
- One tool command per code block (environment exports may share one); the
  sentence above a block says what it does; `KEY=value` listings are tagged
  `ini`.

## Tests

Tier 1 runs for every change. Tier 2 runs for changes that can affect
package-manager or jf compatibility (`api/adapters`, `api/packages`, auth,
storage, formats) and before a merge request. The k8s tier runs for changes to
`helm/`, `deploy/` or the Docker registry, and before a release.

```sh
go tool -modfile=tools/protogen/go.mod buf lint
golangci-lint run ./...
go tool unfold -changed
(cd web && pnpm lint && pnpm typecheck && pnpm test)
task test:core       # tier 1: integration on both engines, then the core scenarios, the SDK suite and the UI tests on the single binary
task test:platform   # tier 2: the platform scenarios on the Compose stack (PostgreSQL, SeaweedFS, a separate worker)
task test:k8s        # one kind cluster: the chart, Kubernetes pulls, restart, upgrade, HA, the restore drill
```

- Every e2e suite is in one Go package: `go test -tags e2e ./test/e2e -run
  TestStack -tier core|platform` starts the tier's Compose stack and the
  containers it needs (testcontainers). `-scenarios 'npm maven'` runs some
  scenarios, `-keep` leaves the stack up, `-url https://…` runs against a
  running server. `-tier k8s` runs `TestKubernetes` (`test/e2e/k8s.sh`).
- Each tier is its own Compose project on free host ports, so tiers run at
  once. Scenarios run in parallel (`-parallel` sets the width).
- Integration tests share one PostgreSQL server when
  `RELIC_TEST_POSTGRESQL_URL` is set (`test/postgresql.sh` starts it; the
  Taskfile does this). The SQLite pass runs only the packages that use a test
  database (`test/database-packages.sh`). The race detector runs in CI when its
  `race` input is set, and with `task test:race`; `task test:all` runs it, then
  tiers 1 and 2.
- CI runs only on demand, to save GitHub Actions minutes: run it once on the
  branch before merging (`gh workflow run ci --ref <branch>`, `-f k8s=true`
  for the k8s tier). Never add automatic triggers.

## JFrog compatibility

Relic serves the Artifactory, Xray, Access, Evidence and Lifecycle APIs in
`api/adapters/artifactory`, so `jf` and jfrog-client-go work against it.
`api/adapters/artifactory/compat/docs/` holds the jf command inventory, the
compatibility matrix and the route inventory.

- **Translation:** the adapter translates JFrog wire types to and from Relic's
  domain types. Artifactory's types never enter the domain; unknown fields go
  under `extensions.artifactory`. Wire names (`rclass`, `packageType`, …) are
  the contract and keep their spelling.
- **JFrog stays in the adapter:** JFrog, its products and clients are named
  only in `api/adapters/artifactory`, the user docs (® on first mention) and
  the landing page. The domain, the Relic API, the UI, the chart and the tools
  speak Relic's terms; what jf needs (storage names, error shapes, auth rules,
  routes) the adapter supplies. `TestJFrogStaysInTheAdapter` enforces it.
  Access tokens use the adapter's format on purpose, so one token works with
  jf and the Relic API.
- **Backward compatible:** the required scenarios, the SDK suite
  (`compat/sdk`, its own module) and the adapter's integration tests stay
  green.
- **Scenarios:** jf scenarios are scripts in `compat/fixtures/scenarios`. A
  scenario joins `compat/e2e/required.txt` in the change that makes it pass
  and never leaves. It is in the core tier unless a `# tier: platform` line
  under its `# covers:` line says why it needs the worker, S3 or PostgreSQL
  (`TestEveryRequiredScenarioHasATier`). A scenario is independent: it uses
  `$REPO`, `$REPO2`, `$BUILD` from `lib.sh` or names of its own, and leaves
  global settings as it found them; one that cannot goes in `e2e.Serial` with
  the reason.
- **The relic CLI matches jf:** every jf command Relic supports has a `relic`
  command that does the job at least as well (shorter, declarative with
  `relic apply -f`, JSON or YAML output). `compat/relicparity/commands.yaml`
  maps each jf command with a scenario to its `relic` command, a `planned`
  one, or the reason none is needed (`TestJfCommandsHaveRelicCommands`). A
  change that makes a jf command work adds its `relic` command or a `planned`
  entry.

## Releases

Versions come from the release tag, never from a file. `helm/relic/Chart.yaml`
stays at `0.0.0`; pushing a `v*` tag runs `.github/workflows/release.yml`,
which stamps the binaries, the images, the embedded UI and the chart it
publishes to `oci://ghcr.io/relicbuild/charts/relic`. A tag with a `-` suffix
is a pre-release: no `latest` image, and not GitHub's latest release.

## Skills

| Task | Skills |
| --- | --- |
| Any Go change | `golang-how-to` (routes to the rest) |
| Relic API, Connect handlers, protos | `golang-grpc`, `golang-error-handling`, `golang-testing` |
| Queries, migrations | `golang-database`, `golang-security` |
| Auth, permissions, uploads | `golang-security`, `golang-safety` |
| The `relic` CLI | `golang-spf13-cobra`, `golang-cli` |
| Linting, CI | `golang-lint`, `golang-continuous-integration` |
| Anything in `web/` | `nuxt-ui` |
| Helm chart, Kubernetes | `kubernetes-skill` |
| Metrics, traces, logs, dashboards, alerts | `golang-observability` |

## Rules

- Never build artifacts into the repository tree: `go run`, or `go build -o`
  a path outside it.
- Never edit generated code (`api/gen`, `web/app/gen`,
  `internal/platform/database/*.sql.go`).
- Never put credentials, hostnames or tokens in repository files, fixtures or
  logs.
- Browser automation never types passwords; ask a person to sign in.
- One merge request at a time: land the open one before starting another.
- When a choice is yours, take the safe default and record it in the commit
  message.
