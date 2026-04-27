# Admin Console UX Blueprint

Status date: 2026-04-26

RelayChat admin UX should converge on a Mattermost-style System Console rather
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

- The current `IntegrationsPanel` has been reshaped into a System Console shell
  with a left navigation tree and right settings panel.
- Compliance policy probes intentionally call Mattermost route shapes:
  data retention, content flagging, compliance reports, groups, schemes, remote
  clusters, and shared channels.
- Access Control / Governance now probes `access_control_policies` search and
  CEL autocomplete fields so the admin UI tracks Mattermost enterprise policy
  capabilities without requiring a full policy engine yet.
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
