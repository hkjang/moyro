# Development Guide

## Prerequisites

- Go matching `server/go.mod`
- Node.js with npm available at `C:\Program Files\nodejs\npm.cmd` on Windows
- Docker Desktop for PostgreSQL, Redis, and MinIO dev services
- PowerShell for the project verification script

## One-Command Launcher

From the repo root:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\dev.ps1
```

The launcher starts the Docker Compose dev services, then opens separate
PowerShell windows for:

- `go run ./cmd/moddle` in `server/`
- `npm run dev -- --host 127.0.0.1 --port 5173` in `webapp/`

It also points `MODDLE_PLUGIN_DIR` at the repo-level `plugins/` directory so
local plugin fixtures are available during development.

Useful switches:

- `-SkipInfra` when PostgreSQL, Redis, and MinIO are already running
- `-NoServer` to start only infrastructure and the web app
- `-NoWeb` to start only infrastructure and the server
- `-WebPort 5174` to move Vite to another port

## Local Services

```powershell
docker compose -f deploy/docker/compose.dev.yaml up -d
```

This starts:

- PostgreSQL on host port `5433`
- Redis on host port `6380`
- MinIO on host ports `9100` and `9101`

## Server

```powershell
Set-Location server
go run ./cmd/moddle
```

Useful environment variables:

- `MODDLE_LISTEN`, default `:8065`
- `MODDLE_DATABASE_URL`, default local PostgreSQL URL
- `MODDLE_REDIS_URL`, default local Redis URL
- `MODDLE_JWT_SECRET`
- `MODDLE_PLUGIN_DIR`, default `./plugins`
- `MODDLE_FILE_BACKEND`, `fs` or `s3`
- `MODDLE_FILE_ROOT`
- `MODDLE_PUBLIC_BASE_URL`
- `MODDLE_ALLOWED_OUTGOING_HOSTS`
- OAuth, SMTP, S3, and link preview variables in `server/internal/config/config.go`

## Webapp

```powershell
Set-Location webapp
& 'C:\Program Files\nodejs\npm.cmd' install
& 'C:\Program Files\nodejs\npm.cmd' run dev
```

In Vite development mode, the login screen auto-signs in with a local dev
account. If the account does not exist yet, the webapp registers it once and
then logs in.

Default dev account:

- username/login: `webuser`
- email: `web@x.com`
- password: `P@ssw0rd1`

Override with `webapp/.env.local`:

```dotenv
VITE_MODDLE_DEV_LOGIN_ID=webuser
VITE_MODDLE_DEV_USERNAME=webuser
VITE_MODDLE_DEV_EMAIL=web@x.com
VITE_MODDLE_DEV_PASSWORD=P@ssw0rd1
```

Disable auto-login when testing the login/OAuth/invite screens:

```dotenv
VITE_MODDLE_DEV_AUTO_LOGIN=false
```

You can also disable it in one browser profile without changing files:

```js
localStorage.setItem("moddle.devAutoLogin.disabled", "true")
```

Prefer TypeScript files in `webapp/src`. The neighboring `.js` files are
legacy/generated artifacts and should not be treated as the source of truth
unless a task explicitly targets them.

## Verification

Run the standard check from the repo root:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\verify.ps1
```

The script uses a workspace-local `.cache/go-build` directory so Go tests do
not depend on a user-level cache directory. This matters in sandboxed Windows
sessions.

For a faster pass:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\verify.ps1 -SkipBuild
```

With a server running:

```powershell
bash scripts/contract-test.sh
```

## Change Rules

- Keep `/api/v4` response shapes stable unless the change is intentionally
  documented.
- Keep plugin hook order deterministic.
- Keep WebSocket reconnect logic single-owner; stale socket events should not
  mutate current state.
- Keep text files UTF-8. PowerShell output may display Korean text incorrectly,
  so inspect actual files before rewriting strings.
- Keep generated output, dependency folders, local caches, and uploaded files
  out of commits.

## Commit Rhythm

Use small commits when a change reaches a verified checkpoint: documentation
baseline, compatibility fix, UX improvement, test expansion, and so on. This
keeps the product moving while leaving a readable trail.
