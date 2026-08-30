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

## 3. Moyro Flow usability

Status: product shell and core workflow surfaces present.

- Global rail and mobile navigation with Today as the authenticated start page
- Unified inbox backed by real unread, mention, approval, and reminder data
- My Work for saved messages, scheduled posts, and reminders
- Standalone approval center, AI assistant, and team-scoped global search
- Workspace sidebar, flat message timeline, scoped-draft composer, and unified
  thread/AI-summary/file/info context panel
- Operator-oriented admin overview and grouped personal/admin navigation
- Route, keyboard, mobile overflow, context focus, and screenshot contracts

Next polish:

- Add durable per-item inbox, task, and decision APIs before enabling their
  prepared UI states.
- Add structured claim/source AI output and server-side retrieval before
  describing summaries as RAG or citation-complete.
- Continue extracting orchestration from `ChatView.tsx` below its 150 KB CI
  ratchet and split the remaining legacy global stylesheet.
- Expand focus management and roving keyboard navigation across older dialogs
  and menus.

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

Status: managed Trusted Native lifecycle plus a tested Mattermost-compatible
subset.

- Server manifest loading
- RPC bridge
- Message hooks
- Slash command delegation
- Web plugin registry
- Admin archive upload/replace, enable/disable, configuration, and delete
- Web plugin bundle serving
- Automated PostgreSQL real-archive functional coverage for Botman 0.1.2,
  Chatdump 0.5.1, Langflow 0.1.20, and EchoSummary 0.6.5 in the local release
  gate (public CI uses the checksum-pinned EchoSummary 0.6.4 asset)

Next polish:

- Plugin logs, health reporting, graceful restart, and runtime error isolation.
- Promote each additional plugin's required SDK and web registry contracts to
  automated compatibility tests before claiming support.
- Expand and document the permission model for plugin APIs.
- Keep Trusted Native trust boundaries visible in every installation path;
  native server executables remain unsandboxed.

## Done Definition

A slice is ready when:

- Code is implemented with existing patterns.
- Docs mention the behavior if it affects architecture, operations, plugins, or
  compatibility.
- `scripts/verify.ps1` passes.
- Any user-facing workflow is checked in a browser when layout or interaction
  changed.
