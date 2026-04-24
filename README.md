# Moddle (RelayChat)

Moddle is a Mattermost-compatible chat platform. The product goal is not a
pixel-for-pixel clone; it is a practical compatibility layer that can run
Mattermost-style clients, bots, webhooks, slash commands, and plugins while
remaining small enough to evolve quickly.

## What Is Here

- `server/` - Go server, `/api/v4` compatibility API, WebSocket gateway, workers
- `webapp/` - React + TypeScript web client
- `plugin-sdk/` - Go and JavaScript plugin SDK experiments
- `plugins/` - local plugin fixtures
- `deploy/` - development Docker Compose services
- `docs/` - architecture, development, plugin, and roadmap notes
- `scripts/` - verification and contract smoke tests

## Current Capabilities

- User registration/login, sessions, profile images, password changes
- Teams, public/private channels, DM/GM channels, membership and archive flows
- Posts, threads, edits, deletes, pins, reactions, markdown, mentions
- File upload/download, thumbnails, custom emoji, image lightbox
- WebSocket events with reconnect reconciliation and unread counters
- Search, saved posts, public channel discovery, link previews
- Incoming/outgoing webhooks, slash commands, bots, personal access tokens
- OAuth provider hooks, invite links, audit logs, metrics, email digest worker
- Scheduled messages and post reminders
- Server plugin host with Mattermost-style RPC hook surface
- Web plugin registry/runtime skeleton

## Quick Start

Start the infrastructure:

```powershell
docker compose -f deploy/docker/compose.dev.yaml up -d
```

Run the server:

```powershell
Set-Location server
go run ./cmd/moddle
```

Run the web app:

```powershell
Set-Location webapp
& 'C:\Program Files\nodejs\npm.cmd' install
& 'C:\Program Files\nodejs\npm.cmd' run dev
```

The server listens on `http://localhost:8065` by default. The Vite web app
uses its configured dev proxy for `/api/v4`.

In Vite development mode the web login screen auto-signs in as
`webuser / P@ssw0rd1`, creating that dev account once if needed. See
[Development Guide](docs/development.md) for overrides and how to disable it
while testing auth flows.

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

## Documentation

- [Documentation Index](docs/index.md)
- [Architecture](docs/architecture.md)
- [Development Guide](docs/development.md)
- [Plugin System](docs/plugin-system.md)
- [Roadmap](docs/roadmap.md)

The older [requirements.md](docs/requirements.md) file is a legacy planning
note with encoding damage. Treat the documents above as the current source of
truth unless and until the legacy note is recovered.
