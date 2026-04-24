# Project Agent Rules

This workspace contains a Mattermost-compatible chat server named Moddle.

## Orientation

- There is no Git repository at the workspace root right now. Inspect files before assuming change history.
- `.claude/settings.local.json` currently contains command permissions only. No Claude hooks or local skills are defined in this workspace.
- Web source lives in `webapp/src` and Vite enters through `webapp/src/main.tsx`. Prefer editing TypeScript/TSX files; the neighboring `.js` files are generated or legacy artifacts unless a task explicitly targets them.
- Server source lives in `server/internal` with the runnable entry point at `server/cmd/moddle`.
- Plugin/server hook flow is `server/internal/pluginhost` -> `server/internal/rpcbridge`. Web plugin runtime flow is `webapp/src/plugins/runtime.ts` -> `registry.ts`.

## Working Rules

- Keep text files UTF-8. PowerShell may print Korean text as mojibake; verify bytes or use the app/editor before rewriting Korean strings.
- Preserve Mattermost-compatible API paths and response shapes under `/api/v4`.
- Keep plugin hook execution deterministic. Do not iterate plugin maps directly for behavioral hooks.
- Keep websocket reconnect behavior single-owner: stale socket events must not update the current socket state.
- Avoid editing `webapp/dist`, `webapp/node_modules`, `webapp/tsconfig.tsbuildinfo`, or `data/files` unless the task specifically requires generated artifacts.

## Verification

- On Windows, the plain `npm` command may resolve to a broken user-level wrapper. Prefer `C:\Program Files\nodejs\npm.cmd`.
- Quick check: `powershell -ExecutionPolicy Bypass -File scripts/verify.ps1`
- Manual web checks:
  - `cd webapp`
  - `& 'C:\Program Files\nodejs\npm.cmd' run typecheck`
  - `& 'C:\Program Files\nodejs\npm.cmd' run build`
- Manual server check:
  - `cd server`
  - `go test ./...`
