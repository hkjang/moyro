# Plugin System

Moyro supports two trusted-native server plugin runtimes behind one lifecycle:

| Runtime | Bundle/source | Compatibility contract |
| --- | --- | --- |
| `moyro_v1` | An existing plugin directory built with `plugin-sdk/go` | Moyro's compact `net/rpc` bridge and hook types |
| `mattermost_v1` | A Mattermost-style `.tar.gz` uploaded at runtime | Mattermost's Go plugin binary handshake and the supported API subset below |

Uploaded archives are installed as `mattermost_v1`. Pre-provisioned plugin
directories that do not yet have a database record retain the `moyro_v1`
default, so existing Moyro SDK plugins continue to load. Runtime kind, enabled
state, bundle digest, last error, installer, and activation timestamps are
stored in PostgreSQL and restored on process restart.

This is compatibility for a tested subset, not a claim that every Mattermost
plugin or every `plugin.API` method is implemented.

## Trust and security boundary

Both runtimes execute **trusted native code**. A server plugin runs with the
same service UID and shares Moyro's container/process namespace, mounted data,
and network. The reduced API exposed to a `mattermost_v1` plugin and the small
environment passed to a `moyro_v1` subprocess improve compatibility and
hygiene; neither is a sandbox or a security boundary.

Only an identity with the `manage_plugins` permission can install, enable,
disable, configure, or delete a plugin; `manage_system` retains recovery
authority. Install only reviewed archives from a controlled source. Moyro does
not currently enforce plugin signatures, isolate filesystem or network access,
or provide per-plugin OS permissions. Installation from a URL and Marketplace
installation remain explicit `501 Not Implemented` compatibility surfaces.

If untrusted native plugin code has run, assume it could access the same
resources as the Moyro service. Rotate at least the PostgreSQL credential,
bootstrap credential, and root encryption key, and follow the deployment's
incident and recovery procedure.

## Bundle and manifest contract

The upload endpoint accepts a gzip-compressed tar archive containing exactly
one top-level directory. That directory name must equal `manifest.id` and must
contain exactly one root manifest:

- `plugin.json`
- `plugin.yaml`
- `plugin.yml`

The manifest requires `id`, `version`, and at least one of `server` or
`webapp`. Plugin IDs must match `^[A-Za-z0-9_.-]{3,190}$`. The current server
binary is resolved from `server.executables["goos-goarch"]`, falling back to
`server.executable`; a web bundle is resolved from `webapp.bundle_path`.

The [official Mattermost plugin starter template](https://github.com/mattermost/mattermost-plugin-starter-template)
packaging convention produces this archive shape. The archive path starts with
the plugin ID; `plugin.json` is inside that directory, not beside it:

```text
com.example.plugin/
├── plugin.json
├── server/
│   └── dist/
│       ├── plugin-linux-amd64
│       └── plugin-linux-arm64
├── webapp/
│   └── dist/
│       └── main.js
├── assets/            # optional
└── public/            # optional
```

Additional manifest-declared platforms may be present. Moyro selects only the
current `goos-goarch` executable and the declared web bundle; their names are
not guessed independently of the manifest.

Archive extraction applies the following policy before anything is moved into
the live plugin directory:

- maximum compressed archive: 256 MiB;
- maximum expanded gzip/tar stream: 512 MiB;
- maximum regular file: 128 MiB;
- maximum entries: 4,096;
- reject empty, NUL-containing, absolute, Windows-drive, backslash, traversal,
  and non-canonical paths;
- reject duplicate entries, including case-insensitive duplicates on Windows;
- accept directories and regular files only; reject symlinks, hard links,
  devices, FIFOs, and other tar entry types;
- ignore archive ownership and mode bits; normalize directories to `0755`,
  regular files to `0644`, and only the selected server binary to `0755`;
- require the selected server binary and web bundle to be regular files inside
  the plugin root, and reject manifest/web/server path overlap.

Extraction occurs in a private `.moyro-plugin-stage-*` directory. Moyro records
the archive SHA-256, then uses a rename into the live directory. Replacing an
existing plugin requires `force=true` (the compatibility alias `replace=true`
is also accepted). Extracted files and every rename boundary are synced before
commit. A PostgreSQL install marker snapshots the previous plugin row,
encrypted configuration envelope, and complete plugin KV namespace. Until the
marker is deleted, the candidate remains hidden from HTTP, hooks, commands,
and web bundle discovery. Activation or persistence failure restores the
previous directory and snapshot; startup performs the same recovery after an
unclean shutdown. Rollback errors are surfaced and leave the marker in place
so Moyro fails the affected plugin closed instead of combining a new binary
with old metadata. Every hook, plugin HTTP request, web bundle response, and
supported `plugin.API` call is pinned to the concrete plugin generation that
admitted it. Replacement, disable, and delete close that generation to new
dispatch, drain admitted work, deactivate it, and only then make the same-ID
candidate available. A stale Mattermost API object is rejected instead of
mutating the candidate's configuration or KV namespace. A generation that
cannot drain within 30 seconds remains unchanged and the lifecycle request
fails as busy. Finalization uses a bounded context independent of the
uploading client's connection. On the supported Linux runtime, `OnActivate`
is limited to 30 seconds; Moyro terminates only the newly launched plugin
process tree and rolls the install back when that deadline is exceeded. If
the upstream RPC call still cannot unwind after termination, Moyro does not
race it with filesystem rollback: the plugin subsystem fails closed, retains
the recovery marker, returns `503`, and requires a process restart. Startup
then restores the prior snapshot before loading any plugin.

## Lifecycle and management API

All management endpoints are below `/api/v4` and require a live bearer/PAT
identity with `manage_plugins` (or the `manage_system` recovery authority):

| Method and path | Behavior |
| --- | --- |
| `GET /plugins` | List manifest, runtime, state, enabled flag, and last error |
| `GET /plugins/capabilities` | Report whether runtime management and uploads are available |
| `POST /plugins` | Install a `.tar.gz`; multipart field `plugin` or a raw gzip body |
| `POST /plugins/{id}/enable` | Persist enabled state and activate the plugin |
| `POST /plugins/{id}/disable` | Deactivate and persist disabled state |
| `DELETE /plugins/{id}` | Deactivate, remove files and persistent plugin/KV state |
| `GET /plugins/{id}/configuration` | Return effective configuration and manifest schema |
| `PUT /plugins/{id}/configuration` | Encrypt, persist, and apply configuration |

`POST /plugins` returns `201` with `id`, `version`, `state`, `enabled`,
`runtime`, `sha256`, and `replaced`. A duplicate without replacement returns
`409`; malformed or unsafe archives return `400`; an unavailable runtime
returns `503`.

Plugin states are `installed`, `running`, and `failed`, with `enabled` stored
separately. Enabled plugins are reloaded and activated after restart. Disable,
enable, delete, and replacement do not require a server restart. Lifecycle and
configuration actions emit `plugin.install`, `plugin.enable`,
`plugin.disable`, `plugin.delete`, and `plugin.configuration.update` audit
events. Configuration audit metadata records keys only, never values.
The live configuration hook is compensating: if it rejects a change, Moyro
restores the previous encrypted envelope and invokes the hook again against the
old effective configuration. An uncertain restore or reapply poisons the
plugin runtime until process restart instead of reporting a clean rollback.

## Configuration and KV persistence

`GET /plugins/{id}/configuration` returns:

```json
{
  "configuration": {"SettingKey": "effective value"},
  "schema": {"settings": []}
}
```

Defaults from `manifest.settings_schema.settings[].default` are overlaid with
saved values. Saved configuration is encrypted with Moyro's root secret
manager and a plugin-specific context before it is written to PostgreSQL. For
a running `mattermost_v1` plugin, a successful update invokes
`OnConfigurationChange`. The HTTP update body is capped at 2 MiB.

Mattermost KV operations use the PostgreSQL `plugin_key_values` table,
namespaced by plugin ID. The supported surface includes get/set/delete,
compare-and-set, compare-and-delete, TTL expiry, delete-all, and paged key
listing. Keys must be valid UTF-8 and at most Mattermost's 150-rune key limit.
Deleting a plugin cascades its KV records.

## Server hooks and HTTP

Both runtimes dispatch activation, message, and command hooks. The Moyro SDK
protocol also defines `OnConfigurationChange`, but the managed configuration
API currently invokes that hook only for a running `mattermost_v1` plugin.
HTTP is bridged through Mattermost `ServeHTTP` or the Moyro `ServeHTTP` RPC:

- `OnActivate`
- `OnDeactivate`
- `MessageWillBePosted`
- `MessageHasBeenPosted`
- `ExecuteCommand`
- `OnConfigurationChange` (live application currently `mattermost_v1` only)
- plugin HTTP (`ServeHTTP` for Mattermost, `ServeHTTP` RPC for Moyro)

Behavioral hooks run in deterministic registration order.
`MessageWillBePosted` threads the latest post through each running plugin and
stops on rejection. `MessageHasBeenPosted` is best-effort because the post has
already committed. Command dispatch stops at the first handled response.

Plugin HTTP is exposed outside `/api/v4` at `/plugins/{id}/...`. The boundary
allows an unauthenticated request because a plugin may implement its own token
scheme. If a Moyro bearer or PAT is supplied, it must validate. Moyro always
removes a client-provided `Mattermost-User-ID` and the reusable
`Authorization` header, then injects `Mattermost-User-ID` only for an
authenticated user. Endpoint-level authorization remains the plugin's
responsibility. Request bodies are capped at 8 MiB; disabled, failed, missing,
or HTTP-less plugins return `404`.

## Supported Mattermost server API subset

The Mattermost runtime preserves the upstream wire ABI needed to start tested
plugins, but Moyro only contracts the following generation-scoped `plugin.API`
operations. Calls from a disabled, deleted, or replaced generation fail rather
than crossing into the current generation:

- configuration: `LoadPluginConfiguration`, `GetPluginConfig`,
  `SavePluginConfig`;
- environment: `GetBundlePath`, `GetServerVersion`;
- commands: `RegisterCommand`, with ownership tied to the registering plugin
  generation;
- users and preferences: `GetUser`, `GetUserByUsername`, `GetUsers`,
  `GetUsersByUsernames`, `GetPreferenceForUser`, `UpdatePreferencesForUser`,
  and `DeletePreferencesForUser`;
- teams and channels: `GetTeam`, `GetTeamsForUser`,
  `GetChannelsForTeamForUser`, `GetChannel`, `GetDirectChannel`,
  and `GetChannelMember`;
- posts: `SearchPostsInTeam`, `CreatePost`, `UpdatePost`, `GetPost`,
  `GetPostThread`, `GetPostsForChannel`, `GetPostsSince`, `GetPostsBefore`,
  `GetPostsAfter`, and `SendEphemeralPost`;
- files: `GetFileInfo` and `GetFile`;
- bots: `GetBot`, `GetBots`, `CreateBot`, `PatchBot`, `UpdateBotActive`, and
  `EnsureBotUser`; bot creation and mutation enforce plugin ownership;
- authorization and membership: `HasPermissionTo`,
  `HasPermissionToChannel`, and `AddUserToChannel`; permission and membership
  checks fail closed outside the implemented cases;
- realtime: `PublishWebSocketEvent`, delivered as a custom plugin WebSocket
  event with the requested broadcast scope;
- logging: `LogDebug`, `LogInfo`, `LogWarn`, `LogError`;
- KV: `KVSet`, `KVSetWithExpiry`, `KVCompareAndSet`, `KVSetWithOptions`,
  `KVGet`, `KVDelete`, `KVCompareAndDelete`, `KVDeleteAll`, and `KVList`.

Plugin API database calls use a 15-second timeout. APIs outside this list are
not part of Moyro's compatibility contract and may be unavailable even though
the embedded upstream interface keeps the binary protocol shape intact.

## Web runtime and discovery

Authenticated users discover enabled, running web bundles with:

```http
GET /api/v4/plugins/webapp
```

The response is an array of `{id, version, url}`, where `url` is the same-origin
`/plugins/{id}/webapp.js` endpoint. The JavaScript endpoint requires a valid
Moyro identity and returns `text/javascript`, `X-Content-Type-Options: nosniff`,
and `Cache-Control: private, max-age=300`.

`PluginLoader` keeps browser bundles aligned with discovery. A bundle registers
through `window.registerPlugin(id, plugin)`. The compatibility runtime exposes
the shared React, ReactDOM, React Redux, Redux, React Router, and PropTypes
packages through `window.React`, `window.ReactDOM`, `window.ReactRedux`,
`window.Redux`, `window.ReactRouterDom`, and `window.PropTypes`. It supports
these registry calls:

- `registerMainMenuAction`
- `registerChannelHeaderButtonAction` (including Mattermost's four-argument form)
- `registerAdminConsoleCustomSetting`
- `registerAdminConsolePlugin` (configuration-schema callback)
- `registerUserSettings`
- `registerPostTypeComponent`
- `registerRightHandSidebarComponent`
- `registerWebSocketEventHandler` / `unregisterWebSocketEventHandler`
- `unregisterAll`

`registerRightHandSidebarComponent` returns the official-style `id`,
`showRHSPlugin`, `hideRHSPlugin`, and `toggleRHSPlugin` action contract. The
Moyro consumers render post-type components, the active RHS, admin callback and
custom-setting contributions, and plugin user-setting sections. Custom
WebSocket registrations receive matching plugin events.

Registry snapshots are reactive. Replacement/unload unregisters contributions
and calls `uninitialize` best-effort. Bundle URLs must be same-origin and below
`/plugins/{matching-id}/`. The runtime attaches the current bearer only to
same-origin plugin and `/api/v4` requests, replaces caller-provided
authorization, strips a caller-provided `Mattermost-User-ID`, and rejects
authenticated redirects so credentials cannot cross origins.

The store facade executes Redux thunks and exposes the bounded
Mattermost-shaped state used by the tested bundles:

- `entities.general.config.SiteURL`
- `entities.users.currentUserId` and `entities.users.profiles`
- `entities.teams.currentTeamId` and `entities.teams.teams`
- `entities.channels.channels`, `entities.channels.currentChannelId`, and
  `entities.channels.membersInChannel`
- `entities.posts.posts`
- `entities.preferences.myPreferences` and
  `entities.preferences.userPreferences`
- `views.rhs.isSidebarOpen`, `views.rhs.pluginId`, and
  `views.rhs.selectedPostId`

The adapter handles received-post and preference actions and reflects reactive
RHS state. These paths and action cases are an explicit compatibility subset,
not the complete Mattermost Redux state or action catalog.

## Explicitly tested Mattermost archives

Compatibility is bounded by plugin, version, and exercised workflow. The
current test/audit set is:

| Plugin | Version | Covered boundary |
| --- | --- | --- |
| Botman | 0.1.2 | PostgreSQL archive E2E: install/activate, web discovery and status HTTP, encrypted configuration with plaintext-secret exclusion, restart, and delete |
| Chatdump | 0.5.1 | PostgreSQL archive E2E: install/activate, web discovery, channel export, schema/config update, same-ID replacement with config preservation, restart, and disable/enable |
| EchoSummary | 0.6.5 local release gate; 0.6.4 public CI fixture | PostgreSQL archive E2E: install/activate, registered slash command, mock vLLM completion, final summary post in a direct-message channel, restart, and disable/enable command ownership |
| Langflow | 0.1.20 | PostgreSQL archive E2E: install/activate, mock Langflow SSE, custom bot post creation/update and completion metadata, custom WebSocket update event, history HTTP, and restart |

The automated test recreates the host against PostgreSQL, installs all four
archives, exercises the listed functional workflows, restarts the host and
confirms all four are running. It also covers Chatdump and EchoSummary
disable/enable behavior, Chatdump replacement, and Botman deletion including
its live directory. Run it with `MOYRO_TEST_POSTGRES_DSN`; each archive path
can be overridden with `MOYRO_TEST_BOTMAN_PLUGIN_ARCHIVE`,
`MOYRO_TEST_CHATDUMP_PLUGIN_ARCHIVE`, `MOYRO_TEST_LANGFLOW_PLUGIN_ARCHIVE`, or
`MOYRO_TEST_ECHOSUMMARY_PLUGIN_ARCHIVE`.

Public CI obtains checksum-pinned release assets with
[`scripts/fetch-plugin-test-fixtures.sh`](../scripts/fetch-plugin-test-fixtures.sh).
It currently pins EchoSummary 0.6.4 because that is the published asset
available to GitHub-hosted runners; the local release gate additionally uses
the sibling checkout's EchoSummary 0.6.5 bundle. The other public fixtures are
Botman 0.1.2, Chatdump 0.5.1, and Langflow 0.1.20.

These named versions and exercised scenarios are the explicit compatibility
boundary. Mock providers make the Langflow and EchoSummary workflows
deterministic but do not prove production behavior of arbitrary external AI
services, every plugin feature, other versions, or unrelated Mattermost
plugins. Additional hooks, API methods, registry surfaces, state paths, and
external-service behavior must be validated individually before production
use.

## Known gaps

- No signature verification, sandbox tier, marketplace, or URL installer.
- No claim of complete Mattermost server API or webapp registry compatibility.
- No per-plugin filesystem/network policy, circuit breaker, or resource quota.
- Native hook execution still shares the Moyro service failure domain.
