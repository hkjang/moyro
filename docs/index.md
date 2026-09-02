# moyro Documentation

This directory is the working product notebook for moyro. Keep documents close
to the code: when behavior changes, update the relevant doc in the same change
set.

## Start Here

- [Project Site](index.html) introduces moyro and links the public guides. The
  published URL is <https://hkjang.github.io/moyro/>.
- [Product Screens](screens.html) presents the 46-view v0.2.1 release gallery,
  including 16 Moyro Flow/context views and four tested plugin compatibility
  states captured by the browser release scenario.
- [User Guide](guides/user-guide.html) covers everyday collaboration flows.
- [Administrator Guide](guides/admin-guide.html) covers service-wide policy,
  authentication, AI, keys, and operations.
- [Offline Deployment](offline-deployment.md) documents the release archive,
  four-variable boot contract, backup, and upgrade procedure.
- [Architecture](architecture.md) explains the runtime shape, core modules, and
  request/event flow.
- [Development Guide](development.md) explains local setup, verification, and
  contribution rules.
- [Plugin System](plugin-system.md) documents the server and web plugin
  surfaces.
- [Mattermost API Compatibility](mattermost-api-compatibility.md) tracks
  official API coverage, gaps, and implementation order.
- [Roadmap](roadmap.md) tracks product maturity and next improvement slices.
- [Project Direction and Goals Analysis](project-direction-and-goals.md) reconstructs
  the original product intent from repository documents, source, and history.
- [Mattermost-compatible OpenAPI](openapi-v4.yaml) documents the `/api/v4`
  compatibility boundary.
- [Moyro Native OpenAPI](openapi-moyro.yaml) documents the focused
  `/api/moyro/v1` product boundary.

## Documentation Rules

- Prefer current, code-backed facts over aspirational detail.
- When describing a partially implemented feature, mark the gap explicitly.
- Keep compatibility notes tied to Mattermost concepts: `/api/v4`, WebSocket
  events, plugin manifests, webhooks, slash commands, and bot/PAT behavior.
- Use only real product captures in `assets/screenshots`; never present a
  generated mockup as an application screenshot.

## Current Product Shape

moyro v0.2.11 presents collaboration as Moyro Flow: **read conversations, make
decisions, and act in one workspace**. A global navigation rail connects
Today, a durable user-scoped activity inbox, the channel workspace, tasks,
decisions, saved/scheduled/reminder data in My Work, approval history and
review, message automation, a permission-aware SSE AI assistant, and live
membership-scoped PostgreSQL knowledge search over messages and documents.
Activity events cover current mentions, direct messages, thread replies,
approvals, reminders, task assignment and supported plugin notifications, with
read, completed and snooze state. The workspace context panel
combines threads, a user-triggered AI summary of currently loaded messages,
files from those messages, and channel information. Knowledge answers retain
stable source cards; AI chat history itself remains browser-session only.

My Work adds task board, calendar, and timeline views, bounded recurrence and
dependency blocking. Decisions support proposal, review, recording, and
supersession history. A typed rule builder turns matching messages into tasks,
decisions, or reminders through a leased PostgreSQL queue. Conversation
documents keep their source thread and watermark so stale summaries can be
identified and regenerated without crossing channel permissions. Inbox rules
cover VIPs, priority, bundling, snooze presets, and timezone-aware work hours.

The platform also provides password and optional Keycloak login, teams and
channels, posts and threads, files, reactions, scoped personal API/MCP keys,
an MCP Streamable HTTP endpoint, and an optional MCP post approval workflow.
The administrator UI covers site/outbound policy, Keycloak, AI, keys and
roles, MCP, approval policy, and an evidence-backed operations overview. It
reads PostgreSQL pool and migration state, durable worker queues, webhook
retry/dead-letter state, and selected file-storage metadata. A missing worker
or dispatcher heartbeat remains `unknown`; an empty queue is never presented
as proof that the runtime is healthy. Other compatibility surfaces may be
partial.
The site policy also controls whether browser drafts use seven-day local
retention by default, current-session storage, or no local storage, and whether
logout clears the current user's stored drafts. The supported v0.2.11 deployment
is one application container with external
PostgreSQL and local file storage. Unconfigured SMTP is exposed as a disabled
capability and does not start a digest worker. S3, Redis fan-out, outbound link
previews, and multi-replica operation are not supported. Reviewed Mattermost
`.tar.gz` archives can be uploaded, replaced, enabled, disabled, configured,
and deleted at runtime. Native plugin executables are fully trusted operator
code and are not sandboxed. Compatibility is limited to the explicitly tested
plugin/version and workflow boundary in [Plugin System](plugin-system.md): all
four named archives now have functional PostgreSQL integration scenarios, with
mock external providers for Langflow and EchoSummary. Public CI pins the
published EchoSummary 0.6.4 archive while the local release gate additionally
tests 0.6.5.

The release also introduces checksummed migrations, scheduled-post leases,
hash-first session lookup, a durable outgoing-webhook delivery queue, and
route-level web code splitting. v0.2.11 keeps the HttpOnly browser session-token
contract and bounded SSO response-loss recovery, and adds restricted guest
access plus idempotent Keycloak group-to-team/channel/role provisioning. Future
HA settings propagation remains outside the single-replica operational
boundary.
