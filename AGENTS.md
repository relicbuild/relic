# AGENTS.md

A guide for coding agents working on Relic. Read it before changing anything;
it says where things live, how the pieces fit, which tools and skills to use,
and what must stay true.

## What Relic is

A self-hosted artifact repository and security platform that speaks
Artifactory's and Xray's APIs, so `jf` (the JFrog CLI) and the package managers
it configures work against it unchanged. It also has its own API (the Relic
API), a web UI and a CLI (`relic`) built on that API.

Sources of truth:

| Document | What it holds |
| --- | --- |
| `api/adapters/artifactory/compat/docs/` | the JFrog command inventory, the compatibility matrix and the route inventory |

## Repository map

```
cmd/relic/              the one binary: relic server start|worker|migrate, and the relic CLI (cobra)
api/
  proto/relic/v1/       the Relic API, in protobuf (the schema of the API and of relic.build/v1 YAML)
  gen/relic/v1/         generated Go (relicv1, relicv1connect); committed, never edited
  apispec/              the Relic API's OpenAPI description (generated with the Go code)
  relicapi/             Connect handlers of the Relic API: proto <-> domain services
  scim/                 SCIM 2.0 provisioning of single sign-on users and groups
  mcpserver/            the read-only MCP tools relic mcp serves
  packages/             the package managers' own protocols (npm, PyPI, Maven raw layout, Go,
                        NuGet, Docker registry, …) served at mounts; the adapter mounts them
                        at its paths, and they never import api/adapters
  adapters/artifactory/ the JFrog-compatible APIs: Artifactory REST and package formats,
                        plus xray/, access/, evidence/, lifecycle/, aql/ (AQL to Relic's
                        item query), layout/ (the storage names jf reads), translators
                        (repoconfig, permissions, ldapsettings, xraypolicy, buildjson, wire),
                        quartz/ (cron expressions), transfer/ (relic import artifactory),
                        integration/ (its integration tests), relic-indexer/, and
    compat/             the compatibility suite: e2e/ (required.txt), fixtures/
                        (jf scenarios), sdk/ (jfrog-client-go suite and payloads/, own
                        module), upstream/, load/, inventory/, routes/ (route inventory),
                        jf.sh, docs/ (inventory, matrix)
internal/               Go packages behind the APIs, grouped into modules:
  app/                  wiring (app.Run) and the shell only it uses: config, backgroundjobs
                        (River workers), openapi, ui (embedded web build)
  platform/             infrastructure with no business rules: database (sqlc), objectstore (S3,
                        Azure Blob, Google Cloud Storage, a local directory), localdisk,
                        telemetry (metrics, tracing, logging), sealing (secrets at
                        rest), servertls (the server's HTTPS certificates), events, httperror, httpserver (router, middleware), scripttest,
                        terminal (the colors, tables and sizes commands print for people)
  access/               who may do what: auth (sessions, tokens, access levels), security
                        (users, groups, permissions), ldap, sso (OIDC, SAML, SCIM directory),
                        workload (workload identities), project, audit
  artifacts/            repositories and what they hold: repository, formats/<format>,
                        itemquery, archive, downloadstats, signingkey, cleanup, usage
  scanning/             scan (scanners, policies, blocking), cvecatalog, endoflife, malicious
  delivery/             buildinfo, release, evidence, replication, webhook
  systemstatus/         health, storage and background job status
queries/                postgresql/: SQL for sqlc (generates internal/platform/database/*.sql.go);
                        sqlite/: the same queries for SQLite (tools/sqlitebridge generates sqlite_queries.go)
migrations/             goose schema migrations, embedded in the binary: postgresql/ and sqlite/
web/                    Nuxt 4 + Nuxt UI v4 single-page app embedded in the binary, served at the root
                        (/): layers/site plus the dashboard (app/)
  app/gen/              generated Connect-ES client of the Relic API; committed
  app/composables/      useRelic (API clients, uploads), useSession, useDebounced
  app/utils/            view models (view-models.ts), mappers from API messages, format, table layout
  app/components/       admin/, artifact/, security/, table/ (TableFilters, TablePagination)
  layers/site/          the landing page, the docs (content/docs), the API reference and install.sh:
                        everything that works without a server; app.config relic.dashboard is false
                        here and true in web/app, and web/app overrides SiteAccountButton
  site/                 relic.build: layers/site alone, prerendered (pnpm generate:site) and served by
                        a Cloudflare Worker (wrangler.jsonc) that Workers Builds deploys from main
deploy/                 docker-compose dev stack (PostgreSQL, SeaweedFS; profile observability),
                        docker-compose.single-node.yml (SQLite and a directory), cloudflare/
  observability/        Grafana dashboards (generated), Prometheus alert rules and their tests,
                        the Compose profile's configuration
helm/relic/             the Helm chart; PostgreSQL (cnpg/cluster with the bundled CloudNativePG
                        operator, CRDs in crds/), SeaweedFS and the optional monitoring stack
                        (monitoringStack.enabled) come from their official charts via Chart.lock
test/                   database-packages.sh (the SQLite pass)
  e2e/                  the e2e suites in one Go package (build tag e2e, testcontainers): the
                        Compose stacks of a tier, the scenarios in the client image (client/), the
                        jfrog-client-go suite, the UI tests, and with -tier k8s one kind cluster
                        for everything Kubernetes-specific (k8s.sh)
  compose/              the plain-HTTP overrides of the Compose stacks the tests start, and checks of their settings
  helm/                 chart render tests (no cluster)
  install/              tests of install.sh
tools/                  protogen (buf and plugins, own module), dashgen, metricsdoc, sqlitebridge,
                        thirdpartynotices (the license texts shipped in the image and the
                        release archives; web/scripts/third-party-notices.ts adds the web UI's)
.opencodereview/        rule.json: the code reviewer's project rules (which documents it reads before
                        reporting, which generated files it skips)
```

## Architecture rules

- **One binary** serves the APIs, the embedded UI and the worker.
- **The Relic API** is ConnectRPC under `/api/`. Every method
  declares `option (relic.v1.access) = PUBLIC | AUTHENTICATED | VIEWER | ADMIN`; an
  interceptor enforces it and fails closed (`TestEveryMethodDeclaresAccess`).
  Errors are Connect codes. Bytes never travel in messages: file contents are
  plain HTTP at `/api/v1/files/{repo}/{path}` and `/api/v1/archive/…`.
- **Adapters** translate JFrog wire types to and from Relic's domain types.
  Do not copy Artifactory's types into the domain; keep unknown Artifactory
  fields under `extensions.artifactory`.
- **Layering**, enforced by depguard in `.golangci.yml`:
  - `api/relicapi`, `api/adapters`, `api/packages`, `api/scim` and `api/mcpserver` never
    import `internal/platform/database`; they use domain services. `api/packages` never
    imports `api/adapters` or `api/relicapi`.
  - `internal/**` (except `internal/app` and tests) never imports `relic/api`.
  - Modules under `internal/` import downwards only:
    `platform` ← `access` ← `artifacts` ← `scanning` ← `delivery` ← `app`, and
    `systemstatus` uses `platform` alone. `platform/httpserver` may use
    `access/auth` (a route's access level); no other `platform` package knows a
    module. A new package goes into the module whose data it owns; a new module
    gets its place in the order and its depguard rule in the same change.
- **Storage:** PostgreSQL through pgx and sqlc, or SQLite for a single node
  (`RELIC_DATABASE_URL=sqlite:///…`), migrations by goose; blobs
  content-addressed in S3-compatible storage, SeaweedFS in development,
  Azure Blob Storage or Google Cloud Storage
  (`RELIC_OBJECT_STORAGE_TYPE=azure|gcs`), or a local directory
  (`RELIC_OBJECT_STORAGE_TYPE=filesystem`);
  background jobs on River; scanners run as subprocesses. Only
  `internal/platform/database` knows the engine.
- **Auth:** Basic, bearer tokens, and the UI's session
  cookie. The cookie is honoured under `/api/` only, and writes made with it
  need the `X-Relic-UI` header (CSRF).

## Tools and versions

| Area | Tools |
| --- | --- |
| Go | Go 1.27, pgx v5, sqlc v1.31, goose v3, River, connectrpc.com/connect, cobra, testcontainers-go |
| Protobuf | buf, protoc-gen-go, protoc-gen-connect-go (pinned in `tools/protogen/go.mod`), protoc-gen-es (web) |
| Web | Nuxt 4, Nuxt UI v4, pnpm, @connectrpc/connect-web, @bufbuild/protobuf v2, zod |
| Lint | golangci-lint v2.15.0 (CI image), `buf lint`, ESLint (`pnpm lint`), `nuxt typecheck` (`pnpm typecheck`), `go tool unfold` ([github.com/imkonsowa/unfold](https://github.com/imkonsowa/unfold), pinned in `go.mod`) |
| Compatibility | `jf` pinned by `api/adapters/artifactory/compat/jf.sh` (2.127.0), jfrog-client-go in `api/adapters/artifactory/compat/sdk` |

## Generated code

Never edit generated files; regenerate and commit them. Drift tests fail when
they are stale.

| Change | Regenerate with | Guarded by |
| --- | --- | --- |
| `api/proto/**` | `go tool -modfile=tools/protogen/go.mod buf generate` (Go code and the OpenAPI description in `api/apispec`) and `cd web && pnpm gen:api` | `TestProtoUpToDate`, CI `web:check` |
| `queries/postgresql/*.sql` | `sqlc generate`, then `go run ./tools/sqlitebridge` | review |
| `queries/sqlite/*.sql` (every PostgreSQL query has one of the same name and kind) | `go run ./tools/sqlitebridge` (writes `internal/platform/database/sqlite_queries.go`; `-check` validates without writing) | `TestSQLiteBridgeUpToDate`, `TestSQLiteQueriesPrepare` |
| HTTP routes | `go run ./api/adapters/artifactory/compat/routes` (writes the route inventory `api/adapters/artifactory/compat/docs/routes.openapi.json`) | `TestOpenAPIUpToDate` |
| jf commands | `go run ./api/adapters/artifactory/compat/inventory generate -cli "$(api/adapters/artifactory/compat/jf.sh)"` | CI `inventory:check` |
| Metrics reference (`web/layers/site/content/docs/3.guides/6.metrics-reference.md`) | `go run ./tools/metricsdoc` | `TestMetricsReferenceUpToDate` |
| Grafana dashboards (`tools/dashgen`), `deploy/observability/prometheus/relic-rules.yaml`, vendored dashboards | `go run ./tools/dashgen` (writes `deploy/observability/grafana/dashboards` and the chart's copies in `helm/relic/files/observability`) | `TestDashboardsUpToDate`, CI `observability:rules` |
| Helm dependencies (`helm/relic/Chart.yaml`) | `helm repo add cnpg https://cloudnative-pg.github.io/charts`, `helm repo add seaweedfs https://seaweedfs.github.io/seaweedfs/helm`, `helm repo add prometheus-community https://prometheus-community.github.io/helm-charts`, `helm repo add grafana https://grafana.github.io/helm-charts`, then `helm dependency update helm/relic` (writes `Chart.lock`; `charts/` is not committed). A new `cloudnative-pg` version also refreshes `helm/relic/crds/cloudnative-pg.yaml` from its `templates/crds/crds.yaml` | `go test ./test/helm/` |
| Round-trip documents from jfrog-client-go and build-info-go types (`api/adapters/artifactory/{repoconfig,permissions,buildjson}/testdata/sdk`) | `cd api/adapters/artifactory/compat/sdk && go run ./payloads` | `TestPayloadsUpToDate` (in that module) |

## Standards

### Naming

Names say what a thing is without their surroundings: `postgresql.enabled`,
not `cluster.enabled`; `RELIC_OBJECT_STORAGE_ENDPOINT`, not `RELIC_S3_ENDPOINT`;
`GetBuildRun`, not `GetRun`; `platform/objectstore`, not `platform/blob`. This
holds for Helm values, environment variables, Go packages and exported
identifiers, protos and routes, and web files and helpers.
Relic has no production users yet: rename
outright, without aliases. Artifactory wire names (`rclass`, `packageType`, …)
are the compatibility contract and stay.

Functions, methods, constants, components and files:

- A noun phrase for what a function returns, or a plain verb for what it does:
  `domainRepository`, `parseActions`, `createParentFolders`, `runHealthCheck`.
- No narrative connectors that say where a value came from or what it is for:
  `repositoryOf` → `domainRepository`, `itemFromRow` → `domainItem`,
  `isHelpFor` → `isHelpText`, `withBlock` → `upsertBlock`. `By` naming a lookup
  key (`FindBySHA256`) and Go idioms (`FromContext`, `ReadAt`, `withTx`) stay.
- Converters follow one pattern: domain → proto is `xMessage`, proto or
  database row → domain is `domainX`, domain → an adapter's wire type is
  `wireX`, text → value is `parseX`.
- No vague words: `helper`, `util`, `manager`, `lookup`, `detail`, `data`,
  `info`, `misc`, `common`, `stuff`, `tmp`, `res`, nor filler verbs (`do`,
  `handle`, `process`, `ensure`, `maybe`) unless they are a library's idiom
  (`singleflight` `Do`, `http.Handle`). `Info` and `Data` stay where a format
  defines them (Go's `.info`, Artifactory's FileInfo, Xray's `general_data`).
- No abbreviations or one- and two-letter names beyond loop variables and
  receivers: `ts` → `optionalTimestamp`, `q` → `promQuery`, `dig` →
  `nestedValue`.
- No decorative words: `safe`, `smart`, `proper`, `simple`, `enhanced`,
  `better`. Say what the code guarantees: `safeJoin` → `joinWithin`.
- Files are named after what they hold (`inventory.go`, not `model.go`;
  `ArtifactPanel.vue`, not `ArtifactDetailPanel.vue`). Framework conventions
  stay: Nuxt's `utils/`, Helm's `_helpers.tpl`, Go's `main.go`.
- Tests named after a function follow its renames
  (`TestFormatOf` → `TestPackageTypeFormat`); tests named as sentences
  describing behaviour stay.
- Timestamps (`createdAt`, `expiresAt`) are data, not narrative, and stay.
  Generated code (`api/gen`, `web/app/gen`, sqlc output) is never renamed by
  hand; rename it at its source (the proto, the query) or leave it.
- Before using a name, check it is free in the package or file scope; rename
  with whole-word matches and review every string literal the rename touched.

### JFrog stays in the adapter

JFrog, its products and its clients are named only in `api/adapters/artifactory`
(and its `compat/` suite), the user docs (`web/layers/site/content`, with ® on first
mention) and the landing page. The domain, the Relic API, the web UI, the
chart and the tooling speak Relic's terms; what jf needs (storage names,
error shape, auth rules, route families) the adapter registers or supplies.
`TestJFrogStaysInTheAdapter` enforces it.
Access tokens are issued in the adapter's format (`access.Format`) on purpose:
one token works with jf and the Relic API, so a token made in the UI can be
given to jf; `internal/access/auth` only takes the format as a parameter.

### The relic CLI does what jf does, better

Every jf command Relic supports has a `relic` command that does the same job
at least as well: shorter to type, declarative where jf is imperative
(`relic apply -f` instead of one command per repository, user or
permission), JSON or YAML output for scripts.
`api/adapters/artifactory/compat/relicparity/commands.yaml`
maps each jf command with an e2e scenario to its `relic` command, a
`planned` one, or the reason none is needed; `TestJfCommandsHaveRelicCommands`
fails when a jf command gains a scenario without an entry, or an entry
names a command relic lacks. A change that makes a jf command work adds
its `relic` command in the same change, or a `planned` entry; planned
entries only ever become `relic` ones.

### Go

- Load the local Go skills through `golang-how-to` and follow them
  (error handling, naming, testing, security, database, and so on).
- Tests first: every change gets a test that fails without it. Integration
  tests carry `//go:build integration` and use `databasetest.NewDB`
  (testcontainers, needs Docker); the server-level ones live in
  `internal/app` with the `server(t)` / `serverDB(t)` helpers.
- Table-driven tests with named cases, `t.Parallel()` where possible.
- Formatting is golangci-lint's: gofumpt, goimports and golines (lines up to
  120 columns, longer calls, literals and signatures one element per line),
  plus wsl_v5 (a blank line around blocks and before a return that follows
  other statements). `golangci-lint fmt ./...` then `golangci-lint run --fix ./...`
  apply both. golines moves a trailing `//nolint` to the closing line of a
  statement it splits, where it no longer applies: put the directive on its
  own line above the statement, or on the line of the offending expression.
  Where golines splits a long expression awkwardly (a call inside a string
  concatenation or an `if` condition), restructure it: break a concatenation
  at `+`, or assign the value first.
- Nothing is inlined: every element of a composite literal (struct, map and
  slice values, even a single one), every field of a struct type and every
  statement of a function literal goes on its own line. `struct{}`, `T{}`
  and `func() {}` stay as they are. `go tool unfold -changed -w`
  applies this to every Go file changed against `origin/main` (then run
  `golangci-lint fmt ./...`); the CI workflow (on every pull request, or on demand) checks the
  files changed since the latest tag, so every file you touch gets unfolded, and the rest of the repo
  follows as it is edited. `go tool unfold -w <dir>/...` unfolds a
  whole tree.
- Errors wrap with context (`%w`); domain errors map to Connect codes in
  `api/relicapi/access.go` (`codeOf`). Never leak internal errors to clients.
- No narrative comments: no history, rationale, justification or restating
  the code; the why goes in the commit message. Exported identifiers and
  packages get a one-sentence doc comment (revive requires it). Keep tool
  directives (`//nolint`, `//go:`, sqlc and goose markers), `// PromQL:`
  comments, and one-sentence notes of a constraint the code cannot express.

### Observability

- A feature is not done until it is observable: new operations get a span
  (`telemetry.Tracer()`, `telemetry.End`) and bounded metrics in the change
  that adds them.
- Declare metrics in `internal/platform/telemetry` (they land in the catalog, the metrics
  reference and the dashboard tests). Write the PromQL that uses a histogram
  in a `// PromQL:` comment above it.
- Label values are bounded: pass request data through the label functions
  (`Family`, `Route`, `Method`, `Format`, `Class`, …), which turn anything
  unknown into `other`. Never a user, path, digest, token or address; the
  repository name only through the opt-in repository families.
- Latency is a histogram observed with `observe`, which attaches the trace ID
  as an exemplar. Log with the `…Context` slog calls so lines carry the trace.
- Dashboards are Go in `tools/dashgen`; alert rules are in
  `deploy/observability/prometheus/relic-rules.yaml` with promtool tests and a
  runbook section in the Monitoring guide each. Regenerate with
  `go run ./tools/dashgen` and `go run ./tools/metricsdoc`.

### SQL and migrations

- Migrations change the schema only: a one-off data conversion never goes
  into `migrations/`.
- The next migration number is **00055**. (Development databases have also
  applied 00016–00019, which were data conversions.)
- A schema change adds the same version to `migrations/postgresql/` and
  `migrations/sqlite/`; `TestSchemasMatch` compares the schemas they build.
- Parameterize every query; keep queries in `queries/postgresql/` for sqlc
  and their SQLite versions in `queries/sqlite/` (same name, kind, `@names`,
  columns in the same order; arrays arrive as JSON for `json_each`, times as
  fixed-width UTC text). Run the integration suite on both engines:
  `RELIC_TEST_DATABASE=sqlite go test -tags=integration ./...`.

### Web

- Load the `nuxt-ui` skill and its routed references before any change in
  `web/`; use Nuxt UI components, semantic colors only (`text-muted`,
  `bg-elevated`, …), never raw palette classes.
- Call the backend through `useRelic()` (generated clients); upload with
  `uploadFile`. (Only the API reference page fetches `/api/v1/openapi.json` directly.)
- Full-page tables follow one layout: `UDashboardPanel :ui="fullBleedPanel"`,
  `<TableFilters>` on top, `UTable sticky :ui="denseTable"`, `<TablePagination>`
  at the bottom (see `pages/security/index.vue`).
- API messages map to view models in `utils/mappers.ts`; keep pages thin.

### Documentation

- User docs live in `web/layers/site/content/docs`. Besides them, the tree
  holds only this guide, a README that says how to run a part, and the
  adapter's generated inventory: no design notes, decision records, plans,
  runbooks or threat models.
- The docs name the server `https://relic.example.com`; the build the server
  embeds shows its own address there (`useServerAddress`).
- One tool command (relic, docker, kubectl, …) per code block; environment
  variable exports may share one. The sentence above a block says what the
  command does; settings listings (`KEY=value`) are tagged `ini`.
- When a choice is yours, take the safe default and record it in the commit
  message.

## Checks before calling work done

```sh
go tool -modfile=tools/protogen/go.mod buf lint
golangci-lint run ./...
go tool unfold -changed                         # no inlined literals, struct types or function literals in touched files
(cd web && pnpm lint && pnpm typecheck && pnpm test)
task test:core                                  # tier 1 (needs Docker): integration on both engines, then on the single binary the core scenarios, the SDK suite, the UI e2e and a storage check
task test:platform                              # tier 2: the platform scenarios and a storage check on the Compose stack (PostgreSQL, SeaweedFS, a separate worker)
task test:k8s                                   # the k8s tier: one kind cluster, the chart, Kubernetes pulls, restart, upgrade, HA and the restore drill (test/e2e/k8s.sh)
```

The tiers split the long checks so nothing runs
twice. Tier 1 runs for every change. Tier 2 runs for changes that can affect
jf or package-manager compatibility (anything under `api/adapters`, auth,
storage, or formats) and before a merge request; `task test:all` runs the
integration suite with the race detector, then tiers 1 and 2 at once. The
k8s tier runs for changes to `helm/`, `deploy/` or the Docker registry, and
before a release; anything Kubernetes-specific goes into `test/e2e/k8s.sh`,
against its one cluster. The SQLite passes run only the packages whose tests use a test
database (`test/database-packages.sh`).

A scenario is added to `api/adapters/artifactory/compat/e2e/required.txt` in
the change that makes it pass, and is never removed. A scenario is in the
core tier unless a `# tier:` line under its `# covers:` line puts it in
`platform` (it needs the separate worker, S3 or PostgreSQL), with the reason; `TestEveryRequiredScenarioHasATier` checks the
line. `go test -tags e2e ./test/e2e -run TestStack -tier <tier>` runs one
tier, and `-scenarios 'npm maven'` the named scenarios of it (`task e2e`);
`-url https://…` runs them against a running server instead of a stack.

The e2e scenarios run in parallel, each a Go subtest running its script in
the client container, so a run takes about as long as its slowest scenario;
`-parallel` sets the width. Every tier has its own Compose project and Docker
picks the published ports, so tiers run at once. A scenario must be
independent: it uses `$REPO`, `$REPO2` and `$BUILD` from `lib.sh` (unique per
scenario) or names of its own, and leaves global settings as it found them.
One that cannot goes in `e2e.Serial` with the reason.

## Skills to load

| Task | Skills |
| --- | --- |
| Any Go change | `golang-how-to` (routes to the rest) |
| Relic API, Connect handlers, protos | `golang-grpc`, `golang-error-handling`, `golang-testing` |
| Queries, migrations | `golang-database`, `golang-security` |
| Auth, permissions, uploads | `golang-security`, `golang-safety` |
| The `relic` CLI | `golang-spf13-cobra`, `golang-cli` |
| Linting, CI | `golang-lint`, `golang-continuous-integration` |
| Anything in `web/` | `nuxt-ui` (plus its routed references) |
| Helm chart, Kubernetes | `kubernetes-skill` |
| Metrics, traces, logs, dashboards, alerts | `golang-observability` |

## Rules

- Stay backward compatible with `jf` and the JFrog Go SDK (jfrog-client-go):
  the required e2e scenarios, the SDK suite (`api/adapters/artifactory/compat/sdk`) and the adapter's
  integration tests stay green.
- Never build artifacts into the repository tree: use `go run`, or
  `go build -o` a path outside it. `go build ./cmd/...` writes a `relic`
  binary into the current directory.
- Never edit generated code (`api/gen`, `web/app/gen`, `internal/platform/database/*.sql.go`).
- Never put credentials, hostnames or tokens in repository files, fixtures or
  logs.
- Browser automation never types passwords; ask a person to sign in.
- One merge request at a time: land the open one before starting another.
- Versions come from the release tag, never from a file: `helm/relic/Chart.yaml`
  stays at `0.0.0`, and the release workflow stamps the binaries, the image,
  the embedded UI and the chart it publishes to `oci://ghcr.io/relicbuild/charts/relic`.
  The docs write Relic's release as `vX.Y.Z` (a chart version as `X.Y.Z`); the
  site shows the newest release there, and a server's UI its own.
