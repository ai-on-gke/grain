# Browser pass at b93646ebc

Core build with the frontend embedded (`cd ui && npm ci && npm run build`, then `go build ./cmd/grain`), on a fresh SQLite dir, with a real Gemini connection and one task. Playwright 1.x, Chromium headless shell. The script signs in by typing the access code (lower case, with spaces) into the login page, then loads each path and searches the page text for team, account, admin, hub and sign-up wording (`switch teams`, `team settings`, `this team`, `team default`, `team's`, `teams`, `members`, `admin mode`, `register`, `password`, `silo hub`, …). It records every request to `/api/teams*`, `/api/users`, `/api/hub/*`, `/api/auth/login` and `/api/auth/register`.

`/` before sign-in: **401**. Requests to removed routes during the whole pass: **none**.

| Path | Status | Landed on | Matches |
|---|---|---|---|
| `/` | 200 | `/` | — |
| `/inbox` | 200 | `/inbox` | — |
| `/processes` | 200 | `/processes` | — |
| `/ci` | 200 | `/ci` | — |
| `/boards` | 200 | `/boards` | — |
| `/templates` | 200 | `/processes` | — |
| `/suites/runs` | 200 | `/processes` | — |
| `/sessions` | 200 | `/sessions` | — |
| `/memory` | 200 | `/settings/memory` | — |
| `/repos` | 200 | `/repos` | "couldn't discover repositories from the team's git connections" |
| `/repos/acme/widgets` | 200 | `/repos/acme/widgets` | — |
| `/repos/acme/widgets/releases` | 200 | `/repos/acme/widgets/releases` | — |
| `/settings` | 200 | `/settings/general` | — |
| `/settings/general` | 200 | `/settings/general` | — |
| `/settings/tasks` | 200 | `/settings/tasks` | — |
| `/settings/models` | 200 | `/settings/models` | — |
| `/settings/git` | 200 | `/settings/git` | — |
| `/settings/credentials` | 200 | `/settings/credentials` | "ims -- backend credentials are set on a team's Models & connections tab, and each capa" |
| `/settings/config-repo` | 200 | `/settings/config-repo` | — |
| `/settings/ci` | 200 | `/settings/ci` | — |
| `/settings/agents` | 200 | `/settings/agents` | — |
| `/settings/sandboxes` | 200 | `/settings/sandboxes` | — |
| `/settings/executors` | 200 | `/settings/executors` | — |
| `/settings/memory` | 200 | `/settings/memory` | — |
| `/settings/access` | 200 | `/settings/access` | — |
| `/settings/engine` | 200 | `/settings/engine` | — |
| `/system` | 200 | `/system` | — |
| `/system/upgrade` | 200 | `/system/upgrade` | — |
| `/system/logs` | 200 | `/system/logs` | — |
| `/system/caches` | 200 | `/system/caches` | — |
| `/system/metrics` | 200 | `/system/metrics` | — |
| `/system/state` | 200 | `/settings/config-repo` | — |
| `/system/accounts` | 200 | `/settings/access` | — |
| `/system/access` | 200 | `/settings/access` | — |
| `/system/models` | 200 | `/settings/models` | — |
| `/system/git` | 200 | `/settings/git` | — |
| `/t/default/tasks` | 200 | `/` | — |
| `/teams` | 200 | `/` | — |
| `/admin` | 200 | `/settings/general` | — |
| `/hub` | 200 | `/` | — |
| `/login` | 200 | `/` | — |
| `/register` | 200 | `/` | — |
