# Admin Detail Screen Plan

Status date: 2026-04-28

RelayChat Admin is a dedicated full-page System Console, not a modal over the
chat workspace. The admin surface excludes general chat and message composition.

## Scope

Primary users:

- Org Owner
- Org Admin
- Workspace Admin
- System Role Admin

Primary menus:

- Organization
- Workspaces
- Members
- Channels
- Apps
- Security
- Audit Logs
- Permissions

Core principles:

- Role-based menu and action visibility.
- Bulk management and export-ready list patterns.
- Auditability for admin actions.
- Fast search, filtering, sorting, and server pagination.
- Sensitive data only for authorized roles.

## Page Structure

- Left navigation: collapsible sections, current page highlighting, disabled
  unauthorized entries.
- Top header: organization name, workspace scope, global admin search, current
  admin account.
- Main body: table-first admin lists with status badges and action toolbars.
- Detail surface: right-side detail panel for selected row metadata and future
  edit actions.
- Action area: refresh, create, disable, export, bulk apply, and destructive
  confirmation actions by page.

## Current Implementation

- `IntegrationsPanel` now renders as a full-page `admin-page`.
- The Admin page uses the scoped Well & Good visual system from
  `docs/admin-design-rules.md`.
- Navigation is reorganized around Organization, Directory, Apps, Security, and
  Operations.
- Organization, Workspaces, Channels, and Apps have table-based admin list
  screens.
- Members, Security, Audit Logs, Permissions, Plugins, Jobs, Invites, Emoji, and
  Webhooks remain connected to existing Mattermost-compatible endpoints.
- A top admin header provides organization scope, workspace scope, global search,
  and administrator identity.
- A reusable right-side detail panel shows selected table row metadata.

## Next Implementation Slices

1. Server pagination contracts for Members, Channels, Apps, and Audit Logs.
2. Column sorting and filter popovers for table headers.
3. Checkbox selection and bulk action confirmation modals.
4. CSV export for Audit Logs and search logs, gated by role.
5. System role matrix for Audit Logs Admin, Users Admin, and Roles Admin.
6. Admin action audit events for every create, update, disable, export, and
   permission-change path.
7. Internationalized strings for Korean and English.
