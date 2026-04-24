# Roadmap

This roadmap is organized around product maturity, not chronological phase
numbers. Each milestone should end with verification and a small commit.

## 1. Foundation

Status: mostly present.

- Auth, sessions, users, teams, channels
- Posts, threads, files, reactions, markdown
- WebSocket events and reconnect recovery
- Local dev services, dev launcher, and verification script
- Basic architecture and development docs

Next polish:

- Keep expanding focused tests around auth, post creation, WebSocket event
  payloads, plugin hook order, and compatibility contracts.
- Split `ChatView.tsx` into smaller feature components without changing UI.
- Make OpenAPI and actual handlers converge.

## 2. Compatibility

Status: active.

- Mattermost-style `/api/v4` routes
- Incoming/outgoing webhooks
- Slash commands
- Bot accounts and PATs
- Plugin manifests and RPC hooks
- API route-shape audit against official Mattermost OpenAPI source

Next polish:

- Drive Mattermost API route-shape coverage upward from the measured baseline
  in [Mattermost API Compatibility](mattermost-api-compatibility.md).
- Add contract tests for webhooks, slash commands, PAT auth, and plugin hooks.
- Document known API differences from Mattermost.
- Add response-shape snapshots for common endpoints.

## 3. Usability

Status: usable but still early.

- Login/register/OAuth/invite flows
- Channel sidebar, DMs, threads, search, saved posts
- Scheduled messages and reminders
- Admin integrations panel

Next polish:

- Improve responsive layouts for tablets and narrow screens.
- Add empty states that help users recover without instructional clutter.
- Improve loading and failure states for slow networks.
- Add keyboard-accessible command paths for power users.

## 4. Operations

Status: partial.

- Health endpoint
- Prometheus metrics
- Optional Redis fanout
- Optional S3-compatible file storage
- Email digest worker

Next polish:

- Add deployment notes for local Docker, single VM, and Kubernetes.
- Add backup/restore guidance for PostgreSQL and file storage.
- Add structured config reference generated from `config.Config`.
- Add graceful plugin restart and health reporting.

## 5. Extension Platform

Status: skeleton plus working server hook path.

- Server manifest loading
- RPC bridge
- Message hooks
- Slash command delegation
- Web plugin registry

Next polish:

- Admin plugin lifecycle: install, enable, disable, configure, logs.
- Web plugin bundle serving and runtime error isolation.
- SDK examples promoted to automated compatibility tests.
- Permission model for plugin APIs.

## Done Definition

A slice is ready when:

- Code is implemented with existing patterns.
- Docs mention the behavior if it affects architecture, operations, plugins, or
  compatibility.
- `scripts/verify.ps1` passes.
- Any user-facing workflow is checked in a browser when layout or interaction
  changed.
