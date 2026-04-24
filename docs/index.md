# Moddle Documentation

This directory is the working product notebook for Moddle. Keep documents close
to the code: when behavior changes, update the relevant doc in the same change
set.

## Start Here

- [Architecture](architecture.md) explains the runtime shape, core modules, and
  request/event flow.
- [Development Guide](development.md) explains local setup, verification, and
  contribution rules.
- [Plugin System](plugin-system.md) documents the server and web plugin
  surfaces.
- [Mattermost API Compatibility](mattermost-api-compatibility.md) tracks
  official API coverage, gaps, and implementation order.
- [Roadmap](roadmap.md) tracks product maturity and next improvement slices.
- [OpenAPI v4 Draft](openapi-v4.yaml) is the current API sketch.

## Documentation Rules

- Prefer current, code-backed facts over aspirational detail.
- When describing a partially implemented feature, mark the gap explicitly.
- Keep compatibility notes tied to Mattermost concepts: `/api/v4`, WebSocket
  events, plugin manifests, webhooks, slash commands, and bot/PAT behavior.
- Avoid storing generated artifacts in docs. Link to source paths instead.

## Current Product Shape

Moddle is already beyond a minimal chat demo: it has auth, teams/channels,
posts, files, reactions, markdown, search, saved posts, webhooks, slash
commands, bots, PATs, custom emoji, OAuth, invites, audit logs, metrics,
scheduled messages, reminders, and plugin host/runtime skeletons.

The next level of maturity is about coherence: stronger docs, deterministic
extension behavior, more predictable UX on narrow screens, better tests around
compatibility contracts, and clearer operational defaults.
