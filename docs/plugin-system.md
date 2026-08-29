# Plugin System

moyro has two extension surfaces: server plugins and web plugins. Both are
intentionally small right now, but their shapes follow Mattermost concepts.

## Server Plugins

Server plugins are discovered from the fixed local plugin directory and loaded
by `server/internal/pluginhost`. The location is not an application runtime
environment variable.

### v0.1.0 trust boundary

Native server plugins are operator-provisioned, fully trusted code in v0.1.0.
They execute with the same service UID and share the container process
namespace, data volume, and network. The launcher supplies only the handshake
cookie in the plugin command environment as defense-in-depth hygiene, but that
does not create a sandbox or isolate service secrets from a malicious binary.

Install only reviewed binaries through a controlled image or volume-provisioning
process. Runtime upload, Marketplace installation, signing enforcement, and
per-plugin permissions are not available in this release. If an unreviewed
plugin has run, treat the PostgreSQL credential, bootstrap password, and root
encryption key as potentially exposed and follow the operator's credential
rotation and recovery procedure.

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

The current protocol identity is `MOYRO_PLUGIN=moyro.v1` with the `Moyro`
RPC service name. Plugin binaries compiled against the earlier pre-moyro
cookie or service name cannot complete the handshake and must be rebuilt with
the current Go SDK. This is source-level portability, not binary ABI
compatibility with Mattermost or an earlier development snapshot.

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

Host invariants:

- Plugin list output follows first registration order, even when a plugin is
  replaced by a newer instance.
- Only running plugins with a live client receive message and command hooks.
- `MessageWillBePosted` threads the latest post JSON through each plugin and
  stops immediately on rejection.
- `ExecuteCommand` skips plugin RPC errors and stops at the first handled
  reply.
- Shutdown deactivates and closes plugin clients in registration order, then
  removes them from the running set.

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
