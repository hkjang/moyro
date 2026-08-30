# moyro Documentation

This directory is the working product notebook for moyro. Keep documents close
to the code: when behavior changes, update the relevant doc in the same change
set.

## Start Here

- [Project Site](index.html) introduces moyro and links the public guides. The
  published URL is <https://hkjang.github.io/moyro/>.
- [Product Screens](screens.html) presents the 41-view v0.2.0 release gallery,
  including 16 Moyro Flow/context views and three tested plugin compatibility
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
- [OpenAPI v4 Draft](openapi-v4.yaml) is the current API sketch.

## Documentation Rules

- Prefer current, code-backed facts over aspirational detail.
- When describing a partially implemented feature, mark the gap explicitly.
- Keep compatibility notes tied to Mattermost concepts: `/api/v4`, WebSocket
  events, plugin manifests, webhooks, slash commands, and bot/PAT behavior.
- Use only real product captures in `assets/screenshots`; never present a
  generated mockup as an application screenshot.

## Current Product Shape

moyro v0.2.0 presents collaboration as Moyro Flow: **read conversations, make
decisions, and act in one workspace**. A global navigation rail connects
Today, a channel-level unified inbox, the channel workspace, saved/scheduled/
reminder data in My Work, approval history and review, a permission-aware SSE
AI assistant, and team-scoped PostgreSQL search. The workspace context panel
combines threads, a user-triggered AI summary of currently loaded messages,
files from those messages, and channel information. It does not claim a
persistent per-event inbox, Task/Decision records, RAG, or durable AI history.

The platform also provides password and optional Keycloak login, teams and
channels, posts and threads, files, reactions, scoped personal API/MCP keys,
an MCP Streamable HTTP endpoint, and an optional MCP post approval workflow.
The administrator UI covers site/outbound policy, Keycloak, AI, keys and
roles, MCP, approval policy, and an operations overview of the status APIs it
can actually read; database-pool, worker-queue, and dead-letter metrics are
not exposed by that dashboard. Other compatibility surfaces may be partial.
The supported v0.2.0 deployment is one application container with external
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
route-level web code splitting. The next level of maturity is about completing
the session-token contract phase and a future HA settings-propagation design
without weakening the current single-replica operational boundary.
