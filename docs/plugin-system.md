# Plugin System

Moddle has two extension surfaces: server plugins and web plugins. Both are
intentionally small right now, but their shapes follow Mattermost concepts.

## Server Plugins

Server plugins are discovered from `MODDLE_PLUGIN_DIR` and loaded by
`server/internal/pluginhost`.

Manifest files:

- `plugin.json`
- `plugin.yaml`
- `plugin.yml`

Required manifest fields:

- `id`
- `version`
- one of `server` or `webapp`

Server executable resolution:

- `server.executables["goos-goarch"]`
- fallback `server.executable`

Runtime flow:

1. Host starts the plugin subprocess.
2. Plugin validates the magic cookie.
3. Plugin writes a HashiCorp-style handshake line to stdout.
4. Host connects over `net/rpc`.
5. Hook payloads travel as JSON inside a small raw gob envelope.

Supported server hook surface:

- `OnActivate`
- `OnDeactivate`
- `OnConfigurationChange`
- `MessageWillBePosted`
- `MessageHasBeenPosted`
- `ServeHTTP`
- `ExecuteCommand`

Behavioral hooks run in deterministic load order. `MessageWillBePosted` can
modify the post JSON or reject the write. `MessageHasBeenPosted` is best-effort
and logs errors because the post has already been persisted.

## Web Plugins

Web plugins are loaded by `webapp/src/plugins/runtime.ts` and register through
`window.registerPlugin(id, plugin)`.

The registry currently supports:

- `registerMainMenuAction`
- `registerChannelHeaderButtonAction`
- `registerPostTypeComponent`
- `registerRightHandSidebarComponent`
- `unregisterAll`

Duplicate web plugin registration replaces prior registry entries and calls
`uninitialize` best-effort before initializing the new plugin instance.

## Current Gaps

- No marketplace installer yet.
- Web plugin bundle discovery is not wired into the admin UI.
- Server plugin configuration persistence is minimal.
- `ServeHTTP` is defined at the RPC layer but needs route exposure policy.
- Plugin SDK examples should become contract tests.

## Product Direction

The next mature version should let admins install, enable, disable, configure,
and inspect plugins without restarting the server. Each plugin should have
clear state, logs, config schema, health, and hook error visibility.
