# Mattermost API Compatibility

Status date: 2026-04-26

Moddle targets Mattermost API v4 compatibility. The official Mattermost
developer documentation describes the REST API as a JSON web service for
clients, integrations, and servers, and points to API version 4 as the current
server API. Mattermost documents endpoints with OpenAPI YAML in the
`mattermost/mattermost` repository under `api/v4/source`.

Reference sources:

- https://developers.mattermost.com/api-documentation/
- https://developers.mattermost.com/contribute/more-info/server/rest-api/
- https://github.com/mattermost/mattermost/tree/master/api/v4/source

## Audit Command

Run from the repo root:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\audit-mattermost-api.ps1 -OutputJson .cache\mattermost-api-audit.json
```

The audit downloads official Mattermost OpenAPI source YAML, extracts
`METHOD /api/v4/path` pairs, extracts local `chi` routes from
`server/internal/httpapi/router.go`, normalizes path parameter names, and
reports matched, missing, and extra route shapes.

## Current Snapshot

Using Mattermost `master` OpenAPI source:

- Official API v4 endpoints: 539
- Local routed endpoints: 404
- Matched endpoint shapes: 358
- Missing official endpoint shapes: 181
- Local-only endpoint shapes: 46
- Route-shape coverage: 66.42%

This is not perfect API compatibility yet. It is a measured compatibility
baseline that can now improve continuously.

Top missing areas by route count:

- users: 31
- channels: 26
- teams: 18
- access_control_policies: 15
- oauth: 14
- cloud: 14
- custom_profile_attributes: 6
- recaps: 6
- reports: 4
- posts: 4
- uploads: 3
- bots: 3

## Compatibility Strategy

Compatibility is tracked in three layers:

- Route shape: method and path exist.
- Behavioral contract: request body, query params, status codes, error
  envelopes, auth rules, and response shape match the OpenAPI contract.
- Client workflow: Mattermost-style web, desktop, mobile, bots, webhooks, and
  slash-command clients can complete real workflows without custom patches.

Route shape is necessary but not enough. Every new compatibility slice should
add route tests or contract smoke tests for success, invalid body, missing auth,
permission denial, and not-found behavior where applicable.

## Development Plan

1. Client boot compatibility
   - Keep expanding `GET /api/v4/config/client`, `GET /api/v4/license/client`,
     `GET /api/v4/system/ping`, and `GET /api/v4/system/timezones`.
   - Add config fields observed from official clients as needed.
   - Make webapp startup depend on the same config surface instead of local
     assumptions.

2. Core user/team/channel/post compatibility
   - Add official aliases where Moddle currently uses `/users/me/...` helper
     routes instead of `/users/{user_id}/...`.
   - Fill high-traffic reads first: teams by id/name, team members, channel
     members by id, channel stats, post by id, file preview/link.
   - Then fill write/update variants with Mattermost-compatible status codes.

3. Preferences and sidebar UX
   - Implement `/users/{user_id}/preferences` and channel category endpoints.
   - Move UI state such as saved posts, sidebar ordering, and notification
     props toward Mattermost-style preference records where practical.

4. Search and autocomplete
   - Add users autocomplete, emoji autocomplete/search, channels search, team
     channels search, and command autocomplete endpoints.
   - Match UI search boxes to these endpoints so API compatibility improves
     real UX at the same time.

5. Integrations
   - Complete incoming/outgoing webhook CRUD route shapes and token regeneration.
   - Add slash command CRUD, dialog open/lookup/submit, and interactive post
     action endpoints.
   - Promote shell contract tests into endpoint-level compatibility fixtures.

6. Admin and enterprise surfaces
   - Keep real operator workflows first: config read/reload, logs, audit,
     plugin inventory, roles, server busy, jobs, license/trial/upgrade status,
     and cluster status.
   - Enterprise/cloud-only surfaces should return compatible disabled/not
     available responses unless the product has a real backing service.
   - Remaining admin-heavy work: audit log certificates, content flagging
     review flows, remote clusters/shared channels, groups, schemes, data
     retention, OAuth apps, import/export, and compliance reports.

7. Plugin/webapp compatibility
   - Add `/plugins/statuses`, `/plugins/webapp`, marketplace read stubs, and
     lifecycle endpoints.
   - Connect admin UI controls to the same route shapes.

## UI/UX Plan

- Startup: show server/version/config-derived capabilities rather than
  hard-coded assumptions.
- Auth: keep dev auto-login only in Vite dev; production should follow the
  official login/config capabilities.
- Navigation: align channel/member/search UI calls with Mattermost route
  shapes, then keep local convenience routes only as internal aliases.
- Errors: surface Mattermost-style `apiError.message` in user-facing toasts and
  preserve detailed errors in console/logs.
- Empty states: use API state to guide recovery, especially for no teams, no
  channels, no search results, missing permissions, and archived channels.

## Recent Progress

- Added an API audit script.
- Added initial client boot compatibility endpoints:
  - `GET /api/v4/config/client`
  - `GET /api/v4/license/client?format=old`
  - `GET /api/v4/system/timezones`
  - `GET /api/v4/config/environment`
- Added official aliases for existing self/team/channel/post workflows:
  - `GET /api/v4/teams`
  - `GET /api/v4/teams/{team_id}`
  - `GET /api/v4/teams/{team_id}/channels`
  - `GET /api/v4/users/{user_id}/teams`
  - `GET /api/v4/users/{user_id}/teams/{team_id}/channels`
  - `GET /api/v4/users/{user_id}/teams/{team_id}/channels/members`
  - `GET /api/v4/users/{user_id}/sessions`
  - `GET /api/v4/channels/{channel_id}/members/{user_id}`
  - `GET /api/v4/posts/{post_id}`
- Added Mattermost preference and sidebar category surfaces:
  - `GET|PUT /api/v4/users/{user_id}/preferences`
  - `GET /api/v4/users/{user_id}/preferences/{category}`
  - `GET /api/v4/users/{user_id}/preferences/{category}/name/{name}`
  - `POST /api/v4/users/{user_id}/preferences/delete`
  - `GET|POST|PUT /api/v4/users/{user_id}/teams/{team_id}/channels/categories`
  - `GET|PUT /api/v4/users/{user_id}/teams/{team_id}/channels/categories/order`
- Added high-traffic users, teams, channels, posts, and command route shapes:
  - `GET /api/v4/users/autocomplete`
  - `POST /api/v4/users/ids`
  - `POST /api/v4/users/usernames`
  - `GET /api/v4/users/email/{email}`
  - `GET /api/v4/users/stats`
  - `GET /api/v4/teams/name/{name}`
  - `GET /api/v4/teams/name/{name}/exists`
  - `GET /api/v4/teams/{team_id}/stats`
  - `GET /api/v4/teams/{team_id}/members`
  - `POST /api/v4/teams/search`
  - `GET /api/v4/channels/{channel_id}/stats`
  - `POST /api/v4/channels/{channel_id}/members/ids`
  - `POST /api/v4/channels/search`
  - `POST /api/v4/posts/ids`
  - `POST|PUT|DELETE /api/v4/posts/schedule`
  - `GET /api/v4/posts/scheduled/team/{team_id}`
  - `GET|POST|PUT|DELETE /api/v4/commands`
  - `GET /api/v4/teams/{team_id}/commands/autocomplete`
- Added auth-chain and read-state compatibility for Mattermost clients:
  - `PUT /api/v4/users/{user_id}/status`
  - `POST /api/v4/users/status/ids`
  - `GET|POST /api/v4/teams/{team_id}/members`
  - `GET /api/v4/teams/{team_id}/members/{user_id}`
  - `POST /api/v4/teams/{team_id}/members/ids`
  - `POST /api/v4/teams/{team_id}/members/batch`
  - `GET /api/v4/users/{user_id}/teams/members`
  - `GET /api/v4/users/{user_id}/teams/unread`
  - `GET /api/v4/users/{user_id}/teams/{team_id}/unread`
  - `GET /api/v4/users/{user_id}/channels`
  - `GET /api/v4/users/{user_id}/channels/{channel_id}/unread`
  - `POST /api/v4/channels/members/{user_id}/view`
- Added media/plugin/emoji compatibility routes that official clients often
  probe during startup and rich-message rendering:
  - `GET /api/v4/users/{user_id}/image/default`
  - `GET /api/v4/files/{file_id}/link`
  - `GET /api/v4/files/{file_id}/preview`
  - `GET /api/v4/plugins/statuses`
  - `GET /api/v4/plugins/webapp`
  - `GET /api/v4/plugins/marketplace`
  - `GET /api/v4/emoji/autocomplete`
  - `POST /api/v4/emoji/search`
  - `POST /api/v4/emoji/names`

## Admin Compatibility Slice

Implemented on 2026-04-26:

- System/config:
  - `GET /api/v4/config`
  - `PUT /api/v4/config`
  - `PUT /api/v4/config/patch`
  - `POST /api/v4/config/reload`
  - `GET|POST|DELETE /api/v4/server_busy`
  - `GET /api/v4/cluster/status`
- Logs/audit:
  - `GET|POST /api/v4/logs`
  - `GET /api/v4/logs/download`
  - `GET /api/v4/audits`
  - `GET /api/v4/users/{user_id}/audits`
- Roles/jobs/plugins:
  - `GET /api/v4/roles`, `GET /api/v4/roles/{role_id}`,
    `GET /api/v4/roles/name/{name}`, `POST /api/v4/roles/names`,
    `PUT /api/v4/roles/{role_id}/patch`
  - `GET|POST /api/v4/jobs`, `GET /api/v4/jobs/{job_id}`,
    `PATCH /api/v4/jobs/{job_id}/status`,
    `POST /api/v4/jobs/{job_id}/cancel`,
    `GET /api/v4/jobs/{job_id}/download`, `GET /api/v4/jobs/type/{type}`
  - Plugin upload/install/enable/disable/delete and marketplace first-visit
    route shapes.
- Enterprise-disabled compatibility:
  - license/trial/upgrade, LDAP certificate/test/sync, SAML certificate and
    metadata, content flagging config, AI bridge, brand image, system notices,
    and support packet route shapes.
- Enterprise/admin compatibility wave:
  - `POST|DELETE /api/v4/audit_logs/certificate`
  - `GET|POST /api/v4/compliance/reports`,
    `GET /api/v4/compliance/reports/{report_id}`,
    `GET /api/v4/compliance/reports/{report_id}/download`
  - content flagging fields, post review state, reviewer search, assign,
    flag, keep, and remove route shapes.
  - data retention global policy, policy CRUD, policy channel/team membership,
    and user channel/team policy read route shapes.
  - groups, schemes, remote clusters, shared channels, and property field/value
    route shapes.
  - interactive dialog open/lookup/submit route shapes for slash-command and
    post-action workflows.

Admin UI now uses these same Mattermost route shapes in the System Console:
system status/config/logs, plugin lifecycle controls, role permission browse,
job creation/cancel smoke controls, and the compliance/policy compatibility
surface.

## Admin Console UX Direction

The admin surface is moving toward a Mattermost-style System Console:

- Entry: separate System Console modal from the chat workspace.
- Navigation: left tree navigation with Environment, Authentication, Site
  Configuration, Integrations, Compliance, and Experimental-style sections.
- Content: dense right-side settings panels built around forms, toggles,
  status rows, and compact operational tables.
- Users and permissions: role-based controls for system, team, and channel
  administration.
- Compliance: logging, audit, data retention, content flagging, reports, and
  remote/shared-channel governance should be grouped together.
- Product stance: prioritize admin control, on-premise operation, and technical
  clarity; use calmer UI density rather than marketing-style layouts.
