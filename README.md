# moyro

moyro is a Mattermost-compatible chat platform. The product goal is not a
pixel-for-pixel clone; it is a practical compatibility layer for
Mattermost-style clients, bots, webhooks, slash commands, and extension
concepts while remaining small enough to evolve quickly. Existing Mattermost
server plugin binaries are not ABI-compatible with moyro's native plugin host.

Product site: <https://hkjang.github.io/moyro/>

## What Is Here

- `server/` - Go server, `/api/v4` compatibility API, WebSocket gateway, workers
- `webapp/` - React + TypeScript web client
- `plugin-sdk/` - Go and JavaScript plugin SDK experiments
- `plugins/` - local plugin fixtures
- `deploy/` - development Docker Compose services
- `docs/` - architecture, development, plugin, and roadmap notes
- `scripts/` - dev launcher, verification, and contract smoke tests

## Current Capabilities

- User registration/login, sessions, profile images, password changes
- Teams, public/private channels, DM/GM channels, membership and archive flows
- Posts, threads, edits, deletes, pins, reactions, markdown, mentions
- File upload/download, thumbnails, custom emoji, image lightbox
- WebSocket events with reconnect reconciliation and unread counters
- Search, saved posts, public channel discovery, and a link-preview foundation
  (outbound previews are disabled by the offline-safe v0.1.1 runtime)
- Incoming/outgoing webhooks, slash commands, bots, personal access tokens
- OAuth compatibility hooks, limited-use invite links, audit logs, and metrics
- Scheduled messages with PostgreSQL leases and duplicate-post prevention, plus post reminders
- Versioned, checksummed PostgreSQL migrations with upgrade/restart validation
- Hashed session lookup identifiers, SMTP capability reporting, and durable
  outgoing-webhook delivery with retry/dead-letter state
- Server plugin host with Mattermost-style RPC hook surface; v0.1.1 loads fully
  trusted, operator-provisioned native plugins at startup and does not claim
  sandboxing, secret isolation, or runtime lifecycle updates
- Web plugin registry/runtime skeleton

## Quick Start

Fast path on Windows:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\dev.ps1
```

That starts the local Docker services, opens the Go server in one PowerShell
window, and opens the Vite web app in another. Use `-SkipInfra` when Docker
services are already running.

Manual path:

Start the infrastructure:

```powershell
docker compose -f deploy/docker/compose.dev.yaml up -d
```

Run the server:

```powershell
Set-Location server
go run ./cmd/moyro
```

Run the web app:

```powershell
Set-Location webapp
& 'C:\Program Files\nodejs\npm.cmd' install
& 'C:\Program Files\nodejs\npm.cmd' run dev
```

The server listens on `http://localhost:8065` by default. The Vite web app
uses its configured dev proxy for `/api/v4`.

The launcher provides the four required server variables and bootstraps
`admin@moyro.local` with the development-only password printed by the script.
See the [Development Guide](docs/development.md) for details.

## Offline Deployment

Published releases contain one loadable service image archive named
`moyro-v<version>.tar.gz`. PostgreSQL is intentionally external and is reached
through `POSTGRES_DSN`; the application itself needs exactly four environment
variables. See the [Offline Deployment Guide](docs/offline-deployment.md) for
the complete load, run, backup, and upgrade procedure. A redacted four-key
template is available at [`deploy/docker/moyro.env.example`](deploy/docker/moyro.env.example).

The supported v0.1.1 topology is one moyro application container connected to
external PostgreSQL, with uploads on the local `/var/lib/moyro` volume. The
four-variable production contract does not expose SMTP configuration, so email
is reported unavailable and no digest worker records false delivery success.
SMTP administration, S3, Redis fan-out, outbound link previews, multi-replica
operation, and runtime plugin installation or enable/disable are not supported
in this release.
Native plugin executables placed in the image or data volume are fully trusted
operator code, not sandboxed extensions. Keycloak OIDC additionally requires
the canonical public Site URL to be saved before the provider is enabled.

## Verification

Use the Windows-friendly project check:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\verify.ps1
```

That runs web typecheck, web production build, and server tests. To skip the
Vite build during a quick pass:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\verify.ps1 -SkipBuild
```

With a running server, smoke-test the API contract:

```powershell
bash scripts/contract-test.sh
```

To audit Mattermost API route-shape compatibility against the official
OpenAPI source:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\audit-mattermost-api.ps1 -OutputJson .cache\mattermost-api-audit.json
```

## Documentation

- [Documentation Index](docs/index.md)
- [Product Site](https://hkjang.github.io/moyro/)
- [Product and Technical Specification](docs/moyro-product-spec.md)
- [Implementation and Verification Checklist](docs/moyro-build-checklist.md)
- [Architecture](docs/architecture.md)
- [Development Guide](docs/development.md)
- [Plugin System](docs/plugin-system.md)
- [Mattermost API Compatibility](docs/mattermost-api-compatibility.md)
- [Roadmap](docs/roadmap.md)
- [Offline Deployment](docs/offline-deployment.md)
- [User Guide](docs/guides/user-guide.html)
- [Administrator Guide](docs/guides/admin-guide.html)
- [Brand Assets](docs/assets/brand/README.md) — canonical SVG mark and wordmark,
  favicon, PWA icons, and the social sharing card

The older [requirements.md](docs/requirements.md) file is a legacy planning
note with encoding damage. Treat the documents above as the current source of
truth unless and until the legacy note is recovered.
