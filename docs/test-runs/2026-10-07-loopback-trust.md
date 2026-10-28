# Loopback trust, the local API token and claimed identities: walkthrough, 2026-10-07

Plan: `docs/test-plans/2026-10-06.md` §2. Task 2380 (filed by task 2231).

## Environment

- The grain sandbox VM: Debian 12, 4 vCPU, 7.8 GiB RAM, with systemd as PID 1. `/dev/kvm` is
  present, and `debian` is in `kvm` and `docker`. docker-ce, Go 1.26.6, node 20 / npm 10, and
  Playwright Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`. `sqlite3` was
  installed with apt to read the stores.
- `/tmp/grain` was built with `go build -o /tmp/grain ./cmd/grain` after
  `cd ui && npm ci && npm run build`, so it embeds the production bundle in `pkg/ui/static`.
- Daemons ran as `debian` on scratch data dirs under `/tmp/lt`, with `-disable-native-sandboxes`
  and no cloud. `grain init` provisioned the real host (`/var/lib/grain`, `grain-daemon.service`,
  account `grain`).

## Tested commit

`candidate` at `dafd69778`, which is this branch's merge base. `git branch -r --contains` lists
`origin/candidate` for all three commits in scope: `de09f3897`, `cbe71f2f0` and `32b99d1e6`.

## Stubs

| Boundary | Stub | Used for |
|---|---|---|
| GitHub | `githubsim.Sim` (`pkg/github/githubsim`) served over plain HTTP by a throwaway 40-line `main` (not committed), plus `git http-backend` for a local bare `acme/widgets`. The daemon (d2) ran with `-github-host 127.0.0.1:18500 -github-insecure-http` and `secrets/github/credentials.json = {"*":"anonymous"}`. | Filing a task on a repo whose CI mode is grain, so the run's `mcpserver` carries `grain-ci`. |
| An agent run | `grain mcpserver` started by hand with the arguments the daemon gives a run, driven over stdio JSON-RPC by a small Python client (`initialize`, `tools/list`, `tools/call`). | Step 1's MCP tools. No model, no sandbox and no dispatch were involved. |
| The Playwright spec | `-github-host 127.0.0.1:9`, an unreachable GitHub, as in `ignored-identity-header.spec.js`. | Nothing in the spec needs GitHub. |

Grain's own APIs and React were not mocked anywhere.

## 1. A daemon with `-ui-trust-loopback=false`

`/tmp/grain daemon -data-dir /tmp/lt/d1 -ui-addr 127.0.0.1:18420 -disable-native-sandboxes -ui-trust-loopback=false`

- **Startup.** The daemon logged `-ui-trust-loopback=false: an unauthenticated request from
  loopback has to sign in; the grain CLI on this host authenticates with the local API token in
  -data-dir/local-api-token`. The token file is `-rw------- debian debian`, and the data dir is
  `0700`.
- **curl.**
  - `/api/tasks` and `/api/teams` with no credential: **401**, with the body `sign in to use this
    server's API: this daemon does not trust loopback (-ui-trust-loopback=false); the grain CLI on
    this host authenticates with the local API token in -data-dir/local-api-token, readable only
    by the account the daemon runs as`.
  - With `Authorization: Bearer $(cat local-api-token)`: **200**, including via `localhost:18420`.
    A wrong token gets 401.
  - `/api/config` stays public: with no credential it answers `actor:""`; with the token it
    answers `actor:"debian", authMethod:"trusted"`.

**Host CLI as the daemon's user** (`GRAIN_SERVER=http://127.0.0.1:18420`):

| Command | Environment | Result |
|---|---|---|
| `grain list` | no token variable | exit 1, the same 401 sentence (see finding 4) |
| `grain list` | `GRAIN_DATA_DIR=/tmp/lt/d1` | exit 0 |
| `grain list` | `GRAIN_LOCAL_API_TOKEN_FILE=…/local-api-token` | exit 0 |
| `grain create -no-repo -title … -body …` | `GRAIN_DATA_DIR` | exit 0. Task 2 was filed with `author=debian creator=debian` |
| `grain ci list` | `GRAIN_DATA_DIR` | exit 0, "no CI runs recorded" |
| `grain metrics` | `GRAIN_DATA_DIR` | exit 0, the full report |
| `grain relay show` | `GRAIN_DATA_DIR` | exit 0. It reads the data dir directly, not the API. |

**Host CLI as another Unix user** (`sudo -u nobody`):

| Command | Environment | Result |
|---|---|---|
| `grain list` | none | exit 1 at once, with the same 401 sentence |
| `grain list` | `GRAIN_DATA_DIR=/tmp/lt/d1` | exit 1 at once, 401 (the file is unreadable, so no token is sent) |
| `grain list` | `GRAIN_LOCAL_API_TOKEN_FILE=…` | exit 1 at once, 401 |
| `grain list` | `GRAIN_USER=debian` | exit 1 at once, 401 |
| `grain -as debian create …` | none | exit 1, 401. Nothing was filed. |
| `curl -H 'X-Forwarded-User: debian' -H 'X-Grain-User: debian'` | none | 401, `names "debian" only in the unsigned X-Forwarded-User header, which this deployment does not trust` |

In every case `nobody` was refused at once: nothing hung, and nothing fell back to acting as
anyone.

**`grain mcpserver`** (d2, which has the GitHub stub). Task 2 is on `acme/widgets`, whose CI mode
is `grain`, and also holds `self-debug`. The server was started with `-sandbox-root … -data-dir
/tmp/lt/d2 -server http://127.0.0.1:18420 -task 2 -pr-repo acme/widgets -pr-branch grain/task-2
-pr-ignore-github-ci`.

- With `GRAIN_LOCAL_API_TOKEN_FILE` (the daemon exports it, so the runs it forks inherit it):
  - `list_ci_runs`: "No grain CI runs are currently remembered."
  - `list_grain_tasks` listed tasks 1 and 2.
  - `read_grain_metrics`: the report.
  - `query_merge_queue`: idle.
  - `update_status`: "no longer has this run in flight", which is expected because no run was
    dispatched.
  - `pull_request_status` and `base_branch_status` answered from the stub.
  - `propose_task` and `comment_on_issue` were "Recorded". These two never call the daemon from
    the run: they go to the run's recorded-tools file and are relayed after it ends.
- Without it: `grain mcpserver: fetching task 2: grain server: sign in … (401 Unauthorized)`. The
  server exits before serving any tool.

## 2. Claimed identity, loopback trusted

d3: `grain daemon -data-dir /tmp/lt/d3 -ui-addr 127.0.0.1:18421 -disable-native-sandboxes`, with
the default `-ui-trust-loopback`. On this daemon the trusted actor is `debian`, which is in
`adminAccounts` (`isAdmin: true`). Team `blue` has alice@example.com (admin) and bob@example.com
(member); the operator was removed from it. bob has a password account and signed in with it.

| Request | Response | Store (`sqlite3 -readonly …/grain.db`) |
|---|---|---|
| trusted `POST /api/tasks {"team":"blue","user":"alice@example.com"}` | 201 | task 1: `origin_actor_id=debian`, `creator=debian` |
| trusted `POST /api/tasks {…,"creator":"alice…","author":"alice…"}` | 201 | task 2: `debian` / `debian` |
| trusted `PUT /api/teams/blue {"user":"alice…", members + mallory}` | 200, acting as the operator, who is a system admin | mallory added, which the operator may do. alice is not recorded anywhere. |
| bob's session `POST /api/tasks {"team":"blue","user"/"creator"/"author":"alice…"}` | 201 | task 3: `bob@example.com` / `bob@example.com` |
| bob's session `PUT /api/teams/blue {"name":"Pwned","user":"alice…", bob → admin}` | **403** `user "bob@example.com" does not have permission to manipulate team "blue"` | `team.name=Blue`; `team_user`: bob is still `member` / `admin=0` |
| bob's session `POST /api/teams/blue/archive {"user":"alice…"}` | **403** | `archived=0` |
| bob's session plus `X-Forwarded-User: alice…` `PUT /api/teams/blue` | **403**, as bob | unchanged |
| trusted `POST /api/tasks/1/comments {"author":"alice…"}` | 200 | `task_comment.author_id=debian` |
| bob's session `POST /api/tasks/3/comments {"author":"alice…"}` | 200 | `task_comment.author_id=bob@example.com` |

No request was ever authorized as, or attributed to, the user its body or a header named. The
refusals wrote nothing. A team admin who files through the UI on a trusted port-forward now files
as the operator (tasks 1 and 2 above). That is the visible change the plan asked to record.

**Frontend `user` fields.** `ui/src` sends no `user` or `creator` in any request body. The one
claimed field left is `author` on the Inbox reply box (`InboxPage.jsx:776`). The server ignores it
(see the comment rows above), so it is dead weight, not a dependency.

## 3. The banner in a browser

New spec: `ui/e2e/loopback-trust-banner.spec.js`. It runs against a real `grain daemon`, because
`grain demo` never computes `LoopbackTrustWarning`; only `pkg/daemon` wires it. Passed 6 of 6 with
`--repeat-each=3`.

| Viewer | Banner |
|---|---|
| Trusted loopback operator, one team and nobody else | none |
| Trusted loopback operator after a second team and two password accounts | **shown** |
| Signed-in system admin (password) | **shown** |
| Signed-in plain member (bob) | none; `/api/config` has no `loopbackTrustWarning` |
| Anyone on a `-ui-trust-loopback=false` daemon (admin or member, signed in) | none |

The banner is a filled warning `Alert` (`role="alert"`) at the top of the banner strip. Its text:

> this deployment trusts loopback (-ui-trust-loopback, the default) and has 4 teams and 3 people:
> anybody whose request reaches the UI/API from this host's loopback -- an SSH port-forward to
> localhost included -- acts as the deployment's own operator without signing in. For a
> multi-user deployment set -ui-trust-loopback=false (GRAIN_UI_TRUST_LOOPBACK=false) and have
> people sign in with a password account or through IAP; the grain CLI on this host keeps working
> through its local API token (grain/task-2078)

It has **no link**: `grain/task-2078` is plain text, and there is no link to docs or settings.
The startup log carries the warning only if the deployment already qualifies when the daemon
starts; `/api/config` re-evaluates it on every request.

## 4. `grain init -ui-trust-loopback=false` on the host

`A` = `-platform=linux-container -repo-root /home/debian/work -data-dir /var/lib/grain`, run with
`sudo /tmp/grain init A …`.

| # | Run | Exit | Outcome |
|---|---|---|---|
| I-1 | `A -ui-trust-loopback=false`, first run | 10 | 53 s. Created the `grain` account, network, build, setcap, claude CLI and chown, then `[done] start the daemon`. `[done] confirm the daemon's API is reachable`, `[done] provision the admin account's password` and `[done] set the default team's worker slots maxWorkers=1` all went through the local API token. The one `[manual]` is the known sandbox budget (3969 MiB cannot hold one 8192 MiB sandbox on this 8 GiB host). The unit has `User=grain` and `ExecStart=… -ui-trust-loopback=false`. `local-api-token` is `-rw------- grain grain`. Unauthenticated curl: 401. |
| I-2 | the same, re-run with the daemon up | 10 | The flag is kept. `[manual] start the daemon: … running the binary this run just replaced -- restart it`. Same budget `[manual]`. |
| I-3 | `A` with **no** flag, daemon up | 10 | The unit is untouched and still `=false`. |
| I-4 | `A` with **no** flag, daemon stopped | 10 | `[done] start the daemon`. The unit was **rewritten without the flag**, and an unauthenticated curl now gets **200** (finding 3). |

CLI on the init host:
- `debian` running `bin/grain list`: 401.
- `sudo -u grain env GRAIN_DATA_DIR=/var/lib/grain bin/grain list`: exit 0.
- root with `GRAIN_LOCAL_API_TOKEN_FILE=/var/lib/grain/local-api-token`: `grain metrics` works.

`grain init` prints no line telling the operator how to run the CLI against the token (finding
4).

## Findings

1. **Fixed: a custom agent backend's `grain mcpserver` cannot reach a
   `-ui-trust-loopback=false` daemon.**
   - Cause: `pkg/agent/custom` starts the backend with a filtered environment (`commandEnv`), and
     hands the mcpserver only `HOME` and `DOCKER_*` through `mcp_server.env`. So
     `GRAIN_LOCAL_API_TOKEN_FILE` never reaches it. The mcpserver sends no token, gets a 401
     fetching its own task, and exits before serving a tool.
   - Reproduction against d2: `env -i PATH=$PATH USER=debian HOME=/home/debian grain mcpserver
     … -server http://127.0.0.1:18420 -task 2 …` gives `fetching task 2: … (401 Unauthorized)`.
     Adding `GRAIN_LOCAL_API_TOKEN_FILE=/tmp/lt/d2/local-api-token` makes `list_ci_runs` answer.
   - Fix: `GRAIN_LOCAL_API_TOKEN_FILE` is added to `mcpServerEnv`. It is a path, not a key, and
     the backend runs as the daemon's account anyway.
   - Regression test: `TestFrameworkHandsTheMCPServerTheLocalAPITokenPath`. It fails without the
     fix (`mcp_server.env = ["HOME=/home/debian"]`).
   - Not verified: the claude, agy and codex frameworks. They pass no `env` of their own. Codex's
     mcpserver is a child of the daemon (`StartCommandHTTP`), so it inherits the variable. Claude
     and agy depend on their CLIs passing their environment on to stdio MCP servers, and neither
     CLI is installed in this sandbox, so that was not exercised.
2. **Proposed: the loopback trust warning fires on a one-person deployment.**
   - Cause: every password account gets a personal team, and `LoopbackTrustWarning` counts teams.
   - Reproduction: start a fresh daemon with trusted loopback (no warning). As the trusted
     operator, `POST /api/auth/register {"username":"debian",…}`. `/api/config` now says "has 2
     teams and 1 people" and the banner stands for good.
   - Also: "1 people".
   - Choosing a better rule is a judgement call. Teams that contain only the operator are also
     what a shared port-forward deployment looks like, so it was not fixed here.
3. **Proposed: a `grain init` re-run that omits `-ui-trust-loopback=false` silently turns loopback
   trust back on** once it rewrites the unit (I-4). It reports `[done] start the daemon` and
   nothing more.
   - Cause: the flag is just an extra daemon argument, so it follows init's "the command line is
     the declaration" rule. A setting that loosens security deserves at least a line, or to be
     kept from the existing unit when it is not given.
   - The spec also avoids a related UI behaviour. A fresh browser on a trusted loopback gets the
     first-time "Create account" dialog with no cancel: `App.jsx`'s `isFirstTimeUser` ignores a
     trusted actor, even though the comment beside it says a trusted caller "is not asked". Many
     demo specs depend on that dialog, so changing it needs a decision.
4. **Proposed: the 401 tells a host user where the token lives but not how to point the CLI at
   it.**
   - The CLI only reads `GRAIN_LOCAL_API_TOKEN_FILE` or `GRAIN_DATA_DIR`. A bare shell, which is
     how a `grain init` host is used since it installs no wrapper, gets the generic sentence.
   - A token file that exists but cannot be read is not mentioned either.
   - The message is readable and the refusal is correct, so this is ergonomics only.

## Fixes on this branch

- `af3fdbcc1` `fix(agent): give a custom backend's mcpserver the local API token path` (finding
  1), with its regression test.
- `9ce46bb60` `test(ui): walk who sees the loopback trust banner in a real browser`, the new
  Playwright spec.

## Checks

- `make security`: green (`go vet ./...`, `tests/security` passed in 121 s). No baseline was
  touched.
- `go test ./pkg/agent/custom/`: ok.
- `npx playwright test e2e/loopback-trust-banner.spec.js --repeat-each=3`: 6 passed.
- Grain CI verdict: see the pull request. The branch's full-suite `wait_for_checks` run is the
  gate.
