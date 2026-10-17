# The read-only demo

`demo.relic.build`: a Relic that visitors browse and pull from as `demo` /
`demo`, a user whose only permission is `read`. It runs on one host as the
Relic system service on SQLite and a local directory, with Caddy in front for
TLS and Cloudflare's proxy in front of Caddy. It has no remote repositories, so
visitors cannot make it download anything.

| File | What it is |
| --- | --- |
| `demo.yaml` | the repositories, the `demo` user, group and permission, the policies and the watch (`relic apply -f`) |
| `seed.sh` | applies `demo.yaml`, then publishes pinned packages from the public registries with each format's client in its official container image |
| `content/` | the demo's own Go module, Helm chart and weak Dockerfile |
| `rebuild.sh` | stops Relic, deletes its data but the vulnerability database, starts it and runs the seed |
| `relic-demo-rebuild.service`, `.timer` | run `rebuild.sh` every Monday at 04:00 UTC |
| `relic.env.demo` | the settings the demo adds to `/etc/relic/relic.env` |
| `Caddyfile` | TLS for `demo.relic.build`, Cloudflare's ranges as trusted proxies |

## Set up a host

On a Debian or Ubuntu host with Docker and Caddy installed, as root.

Install Relic and run it as a system service:

```bash
curl -fsSL https://relic.build/install.sh | RELIC_INSTALL_DIR=/usr/local/bin sh
```

```bash
relic server setup --system --yes
```

Add the demo's settings and an administrator password of your own to
`/etc/relic/relic.env` (`RELIC_ADMIN_PASSWORD=...`), then restart:

```bash
cat relic.env.demo >>/etc/relic/relic.env
```

```bash
systemctl restart relic
```

Copy this directory to `/opt/relic-demo`:

```bash
cp -r . /opt/relic-demo
```

Put the Caddyfile in place:

```bash
cp Caddyfile /etc/caddy/Caddyfile
```

Give Caddy the certificate of Relic's certificate authority, which only the
`relic` user can read where Relic keeps it:

```bash
install -m 0644 /etc/relic/tls/ca.crt /etc/caddy/relic-ca.crt
```

Reload Caddy:

```bash
systemctl reload caddy
```

Install the rebuild service and its timer:

```bash
cp relic-demo-rebuild.service relic-demo-rebuild.timer /etc/systemd/system/
```

Turn on the weekly rebuild:

```bash
systemctl enable --now relic-demo-rebuild.timer
```

Point `demo.relic.build` at the host in Cloudflare (proxied), then seed it once:

```bash
systemctl start relic-demo-rebuild.service
```

## Seed another server

`seed.sh` fills any Relic server as an administrator. The client containers
reach it at `RELIC_SEED_CLIENT_URL`, on the Docker network
`RELIC_SEED_NETWORK` (`host` by default):

```bash
RELIC_URL=https://relic.example.com RELIC_TOKEN=<admin token> ./seed.sh
```
