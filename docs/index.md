# moyro Documentation

This directory is the working product notebook for moyro. Keep documents close
to the code: when behavior changes, update the relevant doc in the same change
set.

## Start Here

- [Project Site](index.html) introduces moyro and links the public guides. The
  published URL is <https://hkjang.github.io/moyro/>.
- [Product Screens](screens.html) shows all v0.1.0 browser-E2E captures.
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

moyro v0.1.0 provides password and optional Keycloak login, teams and
channels, posts and threads, files, reactions, search, saved and scheduled
posts, scoped personal API/MCP keys, OpenAI-compatible streaming AI settings,
an MCP Streamable HTTP endpoint, and an optional MCP post approval workflow.
The administrator UI covers site/outbound policy, Keycloak, AI, keys and
roles, MCP, and approval policy; other compatibility surfaces may be partial.
The supported v0.1.0 deployment is one application container with external
PostgreSQL and local file storage. SMTP, S3, Redis fan-out, outbound link
previews, multi-replica operation, and runtime plugin lifecycle changes are
not supported. Native plugin executables are fully trusted operator code.

The next level of maturity is about deeper compatibility contracts, complete
approval/session regression coverage, and a future HA settings-propagation
design without weakening the current single-replica operational boundary.
