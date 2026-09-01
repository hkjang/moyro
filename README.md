# moyro

moyro is a self-hosted enterprise collaboration platform with a
Mattermost-compatible boundary and built-in AI, MCP, approval, and audit
controls. Its product experience is **one workspace to read conversations,
make decisions, and act**: a global rail connects the Today summary, unified
inbox, channel workspace, personal work, approvals, AI, and search. The goal
is not a pixel-for-pixel clone; Mattermost-style clients, bots, webhooks, slash
commands, and extension concepts remain familiar while the UI can evolve as
Moyro Flow. Moyro supports its native plugin SDK and a tested subset of the
Mattermost v1 server-plugin binary ABI.

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
- Moyro Flow navigation with Today, a durable per-user activity inbox, My Work
  for task boards/calendar/timeline, decision lifecycles,
  saved/scheduled/reminder data, permission-scoped knowledge search, and a
  scoped approval center
- A workspace context panel for threads, user-triggered AI summary of currently
  loaded messages, files from those messages, and channel information
- Search, saved posts, public channel discovery, and a link-preview foundation
  (outbound previews are disabled by the offline-safe v0.2.6 runtime)
- Incoming/outgoing webhooks, slash commands, bots, personal access tokens
- OAuth compatibility hooks, limited-use member and restricted guest invites,
  guest expiry/file policy, audit logs, and metrics
- Scheduled messages with PostgreSQL leases and duplicate-post prevention, plus post reminders
- Versioned, checksummed PostgreSQL migrations with upgrade/restart validation
- Hashed session lookup identifiers, SMTP capability reporting, and durable
  outgoing-webhook delivery with retry/dead-letter state
- Administrator-controlled browser draft policy: seven-day local retention by
  default, session-only storage, or disabled storage, with optional logout cleanup
- Dual server-plugin runtime for Moyro SDK plugins and tested Mattermost v1
  archives, with admin tar.gz install, enable/disable/delete, encrypted config,
  PostgreSQL KV, plugin HTTP, restart recovery, and audit events; all native
  plugins remain fully trusted and unsandboxed
- Authenticated web-plugin discovery and a reactive Mattermost-style registry
- A permission-aware SSE AI assistant plus PostgreSQL knowledge retrieval over
  only the caller's live message/document scope. Answers retain stable source
  cards and work without an external vector service; AI conversation state is
  still browser-session only
- Conversation-derived documents with source-thread watermarks, stale-source
  detection, optimistic revisions, citations, and live membership enforcement
- Conversation-derived tasks and decisions with source-message links,
  priority, recurrence, dependency blocking, board/calendar/timeline views,
  review/supersession history, idempotent creation, audit events, and real-time
  refresh
- Durable per-user message automation rules with bounded typed actions for
  tasks, decisions, and reminders, PostgreSQL leases, retries, and duplicate
  suppression
- Unified-inbox rules for VIPs, event priority, bundling, reusable snooze
  choices, and timezone-aware work hours
- Keycloak group-to-team/channel/role mappings that provision SSO users
  idempotently while retaining current membership checks on every request
- Evidence-backed administrator operations state for PostgreSQL pool and
  migrations, durable queues, webhook retry/DLQ, and the selected file-storage
  backend; unobservable worker/dispatcher runtime remains explicitly unknown

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

The supported v0.2.6 topology is one moyro application container connected to
external PostgreSQL, with uploads on the local `/var/lib/moyro` volume. The
four-variable production contract does not expose SMTP configuration, so email
is reported unavailable and no digest worker records false delivery success.
SMTP administration, S3, Redis fan-out, outbound link previews, and
multi-replica operation are not supported in this release. Runtime plugin
upload and lifecycle management are supported for reviewed Mattermost-style
archives; Marketplace and URL installation are not.
Native plugin executables, whether uploaded or pre-provisioned, are fully
trusted operator code, not sandboxed extensions. Keycloak OIDC additionally
requires the canonical public Site URL to be saved before the provider is
enabled; its connection probe validates discovery and the advertised JWKS.
An HTTPS issuer rejects HTTP token, JWKS, and UserInfo endpoints by default.
Administrators may explicitly allow those back-channel endpoints only for an
isolated, trusted private network; the browser-facing authorization endpoint
remains HTTPS-only and the setting warns that secrets and codes cross HTTP in
plaintext and that the traffic, including JWKS, can be intercepted or modified.
After a successful provider callback, v0.2.6 sends the browser a five-minute,
browser-bound handoff code instead of a session JWT. The atomic exchange sets
the reusable login credential only in an HttpOnly, SameSite cookie and returns
the local user without exposing that credential to JavaScript. If the exchange
response is lost, the same browser may retry for 60 seconds and receives the
exact same session; late or differently bound retries are rejected. Browser UI
requests use the cookie, while API clients and personal access tokens keep the
Bearer contract.

The PostgreSQL real-archive E2E boundary is Botman 0.1.2, Chatdump 0.5.1,
Langflow 0.1.20, and EchoSummary 0.6.5 in the local release gate. Public CI
uses the checksum-pinned published EchoSummary 0.6.4 archive. The exercised
status/config, export/replace, mock Langflow SSE bot, and mock vLLM slash/DM
workflows are not a promise that arbitrary Mattermost plugins or every feature
of these plugins is compatible. See the [Plugin System](docs/plugin-system.md).

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
- [Mattermost-compatible OpenAPI](docs/openapi-v4.yaml) — `/api/v4`
- [Moyro Native OpenAPI](docs/openapi-moyro.yaml) — `/api/moyro/v1`
- [Roadmap](docs/roadmap.md)
- [Offline Deployment](docs/offline-deployment.md)
- [User Guide](docs/guides/user-guide.html)
- [Administrator Guide](docs/guides/admin-guide.html)
- [Product Screens](docs/screens.html) — 46 browser-captured v0.2.1 views,
  including the 16 Moyro Flow/context views and four tested plugin
  compatibility states
- [Brand Assets](docs/assets/brand/README.md) — canonical SVG mark and wordmark,
  favicon, PWA icons, and the social sharing card

The older [requirements.md](docs/requirements.md) file is a legacy planning
note with encoding damage. Treat the documents above as the current source of
truth unless and until the legacy note is recovered.
