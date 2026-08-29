# Admin Console UX Blueprint

Status date: 2026-04-26

moyro admin UX should converge on a Mattermost-style System Console rather
than a chat-adjacent utility drawer.

## Structure

- Entry: separate System Console surface, opened from the app shell.
- Layout: left tree navigation plus right settings panel.
- Density: high information density, optimized for system administrators and
  DevOps operators.
- Interaction model: form inputs, toggles, compact tables, and explicit save or
  action buttons.
- Tone: technical and control-oriented, with clear state and validation.

## Information Architecture

- Environment
  - Web Server
  - Database
  - File Storage
  - Background Jobs
- Authentication
  - Users
  - Roles and Permissions
  - LDAP, SSO, MFA when backed by real services
- Site Configuration
  - Teams
  - Invites
  - Custom Emoji
  - Permission defaults
- Integrations
  - Incoming Webhooks
  - Outgoing Webhooks
  - Bots and personal access tokens
  - Plugins
- Compliance
  - Logs and audit trails
  - Data retention
  - Content flagging
  - Compliance reports
  - Shared-channel and remote-cluster governance
- Experimental
  - Feature flags
  - Compatibility-only route smoke controls

## Current Implementation Notes

- The current `IntegrationsPanel` has been reshaped into a dedicated full-page
  System Console with a left navigation tree and right settings panel.
- Admin is intentionally not a modal over chat; opening it switches to an
  operator page surface.
- The admin page now has organization/workspace scope, global admin search,
  current-admin identity, collapsible navigation sections, table-based detail
  lists, and a right-side detail panel.
- Compliance policy probes intentionally call Mattermost route shapes:
  data retention, content flagging, compliance reports, groups, schemes, remote
  clusters, and shared channels.
- Access Control / Governance now probes `access_control_policies` search and
  CEL autocomplete fields so the admin UI tracks Mattermost enterprise policy
  capabilities without requiring a full policy engine yet.
- The console panel now shows an active section/page header and stacks the tree
  navigation above the settings panel on narrow viewports.
- Authentication now has its own operator page backed by Mattermost-compatible
  license, LDAP, SAML, MFA/config, and session-policy probes.
- Enterprise-only features that do not have backing services should remain
  compatible disabled surfaces until real storage and workflows exist.

## Next UX Work

- Split the console into smaller page components once the tree grows past the
  current compatibility/admin slice.
- Move authentication policy controls into a dedicated Authentication section.
- Add validation summaries near forms while preserving detailed technical error
  text from Mattermost-style API errors.
- Add row-level empty states for policy areas that explain current operational
  state without hiding the route compatibility status.
