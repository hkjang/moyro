# Web plugin consumer API

Plugin bundles register through `window.registerPlugin`. The compatibility
runtime supplies `window.React`, `window.PropTypes`, and a live store adapter
with these Mattermost-shaped paths:

- `store.getState().entities.general.config.SiteURL`
- `store.getState().entities.channels.channels`
- `store.getState().entities.channels.currentChannelId`

UI consumers should import `usePluginRegistryState` from `./registry`. It uses
an immutable external-store snapshot and rerenders whenever a plugin registers,
is replaced, or unregisters. The relevant collections are:

- `channelHeaderButtons`: render the icon/tooltip and invoke `action` from the
  channel header with the current channel/member arguments when available.
- `adminConsoleCustomSettings`: match `pluginId` and manifest setting `key`,
  then render `component` with the Mattermost custom-setting props.
- `mainMenuActions`, `postTypeComponents`, and `rhsComponents`: existing
  registration surfaces, now reactive through the same hook.

`loadPluginBundle` accepts only same-origin URLs rooted at
`/plugins/<matching-plugin-id>/`. Fetches to same-origin `/plugins/` routes use
the current Moyro bearer token; caller-provided authorization is never trusted,
external URLs never receive the token, and authenticated redirects are blocked.
