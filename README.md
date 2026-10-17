# Relic

Relic is a self-hosted artifact repository and security platform.
One binary keeps your packages and images in S3-compatible storage and scans everything it stores.

## Install

Install the latest release into `~/.local/bin`:

```bash
curl -fsSL https://relic.build/install.sh | sh
```

Run it as a service on this machine, with SQLite and a local directory:

```bash
relic server setup
```

Or build it from source (Go 1.27, Node 24 and pnpm), with the web UI:

```bash
task install
```

Without the web UI:

```bash
task cli:install
```

Docker images are at `ghcr.io/relicbuild/relic` and on Docker Hub as
`relicbuild/relic`, and the Helm chart is in `helm/relic`. The documentation at [relic.build/docs](https://relic.build/docs)
covers each way to run Relic.

## Development

Run a single node on `:8090` (SQLite and a local directory, admin password
`password`):

```bash
task run
```

Run the UI dev server, which proxies the API to `:8090`:

```bash
task web
```

Run the relic.build site (landing page and docs, no dashboard):

```bash
task web:site
```

Run the tests:

```bash
task test
```
