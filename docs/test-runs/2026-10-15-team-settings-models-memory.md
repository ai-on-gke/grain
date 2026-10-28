# Team settings: Models & connections, Memory page, per-connection CLI, onboarding steps — 2026-10-15

Task: grain/eu/task-3945 (bwsalmon/grain, base `candidate`), filed by the
nightly test-plan task 3749. Plan: `docs/test-plans/2026-10-15.md` §2.

## Environment

- Commit tested: `591f0f35f` (tip of `candidate` when the branch was cut).
  All eleven commits under test are on `origin/candidate` and
  `origin/main` (`git branch -r --contains`): `19da821c8`, `80e6bb160`,
  `85e06ec18`, `226f111a2`, `f3a92406f`, `47c383e1f`, `e0a76cc05`,
  `d50be1c18`, `6c74c8340`, `d6c53db9f`, `5e6884a54`.
- `cd ui && npm ci && npm run build`, then
  `go build -tags grain_e2e -o /tmp/grain-e2e ./cmd/grain` so the binary
  embeds the built frontend and has the local host-directory sandbox
  backend the e2e suite uses (`pkg/daemon/local_sandboxes_e2e.go`).
- Browser steps: `grain demo -addr 127.0.0.1:8765 -open=false` (its own
  throwaway SQLite store), driven by ad hoc Playwright scripts. Readiness
  was polled on `/api/sandboxes`. Teams added through the API:
  `alpha` "Alpha Squad", `beta` "Beta Crew", and `solo` "Solo Shop".
  Users registered through `POST /api/auth/register`: `carol`, who is
  admin of alpha and solo, and `bob`, who is a plain member of alpha,
  admin of beta, and briefly a viewer of solo. Non-admin steps ran with no
  `X-Grain-Admin-Mode` header (the `extraHTTPHeaders: {}` equivalent).
- Daemon steps: a real `grain daemon` (same binary) on a scratch data dir
  `/tmp/step3/data`, with `-sandbox-dir /tmp/step3/sandboxes`,
  `-poll-interval 200ms`, `-max-workers 1`, `-max-mergers 0`,
  `-agent-framework claude`, `-github-host 127.0.0.1:9` (no GitHub), and
  `-as alice` (loopback). Tasks targeted the deployment's own
  `grain/config` repository, which needs no GitHub.
- Playwright: Chromium 151 (`chromium-1234` and its headless shell),
  installed with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.

### How the saved password for step 2 was arranged

This is a real saved password, filled in by Chromium's own password
manager rather than by the script:

1. `chromium.launchPersistentContext(dir, { channel: "chromium",
   args: ["--password-store=basic"] })` starts full Chromium in new
   headless mode with a persistent profile. The bare headless shell has no
   password manager.
2. Chromium creates its own `Default/Login Data`. Then
   `/tmp/seedpw.py` inserts one `logins` row: origin and signon realm
   `http://127.0.0.1:8765/`, username `carol`, and the password encrypted
   the way the Linux basic store does it. That is `v10` plus AES-128-CBC
   with key PBKDF2-SHA1("peanuts", "saltysalt", 1 iteration) and an IV of
   16 spaces, done with `openssl enc`.
3. Positive control: on a fresh copy of that profile, grain's sign-in
   dialog came up with Username `carol` and a 21-character Password
   already filled in by Chromium. Choosing "Log in" enabled the button,
   and signing in with those values worked. So this browser does
   autofill grain's password on this origin.

## Steps and outcomes

### 1. Old links, section order, empty states — Pass

- As carol, `/t/alpha/settings/connections` and `/t/alpha/settings/agents`
  each open Team settings for alpha with the "Models & connections" tab
  selected. The URL is left as typed, and the team is kept.
- Section order, as headings: "provider connections", "models", "models
  and the connections that serve them". Connections come first.
- Empty states: connections say 'Team "Alpha Squad (alpha)" has no provider
  connection, so its tasks wait. Create one…'. The model table says 'No
  connection of team "Alpha Squad (alpha)" advertises a model yet. Add a
  connection above…', which points at the section that really is above
  it.

### 2. No unsaved-changes prompt after a save, with a saved password — Pass

The browser profile is the one described above. Steps: sign in through
grain's dialog with the autofilled credentials, open
`/t/alpha/settings/models`, and press Edit on `alpha-claude` (a Claude
connection with "Claude Code OAuth token" authentication, so the
credential box is drawn). Then click inside the form, which counts as a
user gesture and releases any autofilled value to the page. Set
Concurrency to 2, Save, and press "← Models & connections".

- The credential box is `autocomplete="new-password"` and was empty
  (length 0) on opening and after Save.
- No `beforeunload`/`confirm` dialog appeared (the `dialog` event list was
  empty), and the page returned to the tab.
- With authentication left unchosen, the page draws no password box at
  all, so the case only arises once a credential method is picked.

### 3. Each connection's own CLI, runs and discovery, missing path — Pass after one fix

Stubs: `/tmp/step3/cli-a/claude` and `/tmp/step3/cli-b/claude` (see
Stubs). Connections `claude-a` and `claude-b` in team `default` each
advertise their own model (`claude-model-a`, `claude-model-b`) and set
`cliPath` to their own stub. Each has an OAuth-token credential set
through `PUT /api/agent-backends/{id}/credential`.

- Runs: task 2 (`model: claude-model-a`) and task 3 (`claude-model-b`), on
  `grain/config` and approved. `cli-runs.log` recorded
  `/tmp/step3/cli-a/claude model=claude-model-a`, then
  `/tmp/step3/cli-b/claude model=claude-model-b`. Each attempt
  `succeeded` ("the run made 1 tool call(s)"). **Pass.**
- Saving a `cliPath` that does not exist is refused with 400:
  `agentBackends[2].cliPath: /tmp/step3/missing/claude is not an executable file the daemon's account can run: … no such file or directory`.
  **Pass.**
- Missing at run time: claude-a was pointed at a copy of its stub, and the
  copy was then deleted. Task 4's attempts end `setup-failed` with
  `this run's sandbox could not be prepared: orchestrator: building the agent framework for run 4-1: connection "claude-a"'s CLI path "/tmp/step3/cli-gone/claude" cannot be run here: … no such file or directory -- fix or clear it in Team settings -> Models & connections`.
  The task page shows that same line for each attempt, and retries back
  off (30s, then 60s). **Pass.** The "sandbox could not be prepared"
  framing is slightly off for a missing CLI, but the sentence after it
  says exactly what is wrong.
- Discovery for Claude does not run the CLI by design
  (`pkg/daemon/claude_models.go` asks Anthropic's API), so `cliPath`
  cannot show there. Both Claude connections answered with Anthropic's
  real `HTTP 401: OAuth access token is invalid` for the stub tokens.
  Per-connection discovery was therefore walked with two Codex
  connections, `codex-a` and `codex-b`, whose `cliPath`s point at stub
  `codex` scripts answering `app-server` `model/list`. Once the daemon
  had applied them, each listed its own model, and `cli-runs.log`
  recorded `/tmp/step3/codex-a/codex app-server` and
  `/tmp/step3/codex-b/codex app-server`. A deleted Codex CLI shows on
  the connection page as 'Could not list this provider's models:
  connection "codex-a"'s CLI path "…/codex-gone/codex" cannot be run
  here: … -- fix or clear it in Team settings -> Models & connections'.
- **Finding 2 (fixed):** discovery right after a save used the wrong CLI.
  See Findings.

### 4. onboardingDismissed — Pass

- Demo, as carol (sole admin of `solo`): the team home checklist showed
  "Add members: not needed". Pressing it, then reloading, leaves "Undo
  Not needed for Add members" showing and `onboardingDismissed:
  ["members"]`. Undo, then reload, brings the button back and the field
  is cleared. For alpha, which already has three members, the step is done
  and offers no dismiss, as designed.
- As bob, `PUT /api/teams/alpha {"onboardingDismissed":[]}` (plain member)
  → **403** `user "bob" does not have permission to manipulate team
  "alpha"`. As a viewer of solo it is also 403. As admin of beta it is
  200.
- Real daemon: `PUT /api/teams/alpha {"onboardingDismissed":["members"]}`,
  then:
  - SIGTERM and restart on the same data dir: still `["members"]`.
  - `POST /api/state-repo/sync` exports it (`tables/team.json`:
    `"onboarding_dismissed": "members"`). A daemon started on a fresh
    data dir holding only a copy of that exported state repository
    ("importing its settings") reads `["members"]`.
  - Clearing the field in `tables/team.json`, committing it as a merged
    grain/config change would, and restarting imports it: the field is
    gone.

### 5. Memory page — Pass in the browser; Finding 1 (fixed) in the API behind it

- As carol, `/t/alpha/memory` shows "This deployment", read only for a
  non-system-admin, with its documents. It then shows "Team alpha" with
  alpha's own note (`ALPHA-DEPLOY-NOTE…`, editable for carol) and alpha's
  document `alpha-runbook`, plus "Another repo". Beta's note and
  document are not shown.
- The introduction points at the standing instructions under "System →
  Agents & Task Branches", which is the real System tab
  (`AdminOverlay.jsx`, id `agents`, whose field is "Standing
  instructions for every run").
- **Finding 1 (fixed):** the API sent beta's memory to carol anyway. See
  Findings.
- Observation, not changed: the Memory page's scope headings name the
  team by id ("Team alpha"), where Team settings now uses "Alpha Squad
  (alpha)". `MemoryPage.test.jsx` pins the id form ("Team team-alpha"),
  so changing it is a wording decision, left for whoever owns that page.

### 6. Display name in Team settings headings — Pass after one fix

Every Team settings tab was swept for lines naming `alpha`. They all say
"Showing settings for team: Alpha Squad (alpha)". Models & connections and
Executors say 'team "Alpha Squad (alpha)"'. The Team tab names the id
deliberately ("Its id, alpha, is what the API… name it by") and gives the
config repo `grain/config-alpha`.

- **Finding 3 (fixed):** Audit / security said 'used by work in team
  "alpha"'.

## Findings

### 1. `GET /api/memory` gave a member every team's memory — fixed

Reproduction, on `grain demo`: create teams alpha and beta, put a team
note in each (`PUT /api/memory {"team":"beta","text":"BETA-DEPLOY-NOTE…"}`),
and register `carol` as a member of alpha only. Then sign in as carol and
run `GET /api/memory`. Beta's note is in the response. A user in no team
at all got every team's notes as well.

Cause: `handleListMemories` passes `("", <caller's teams>...)` to
`Store.ListMemories`, which read only `teamFilter[0]` and treated `""` as
"no filter". The documents listing beside it filters per scope and was
not affected. The Memory page hid this because `memoryScopes` drops other
teams' rows client-side.

Fix: `ListMemories` selects `team IN (...)` over every entry
(`pkg/model/memory_store.go`). Regression test:
`pkg/ui/memory_test.go` `TestListMemoriesShowsAMemberOnlyTheirOwnTeams`.
It fails on the old code with `bob: GET /api/memory = ["deployment note"
"alpha note" "beta note"]`.

### 2. Model discovery ignored a just-saved connection's `cliPath` — fixed

Reproduction, on a real daemon with `-poll-interval` 200ms: save
connection `codex-c` with `cliPath` set to a stub codex, then immediately
call `GET /api/agent-models?framework=codex&backend=codex-c`. The answer
is `the Codex CLI is not installed; type a model name instead: exec:
"codex": executable file not found in $PATH`, and the second request,
one tick later, lists the stub's model. Re-pointing `codex-a` at another
binary and asking at once listed the old binary's model. The connection
page asks for its models the moment it saves, and the default poll
interval is 10s, so a real deployment shows the wrong answer for up to
about 10 seconds after every save.

Cause: the Codex, Antigravity and Claude model listers read
`LiveConfig.Current()`, which adopts stored connections only on the
reconcile tick. Dispatch reads them on every call (`DispatchConfig`),
which is why runs were never affected.

Fix: the listers read `LiveConfig.modelListingConfig(ctx)`, which is
`DispatchConfig` over the applied config (`pkg/daemon/live_config.go`,
`agent_models.go`, `codex_models.go`, `claude_models.go`). Regression
test: `cmd/grain/daemon_cli_path_test.go`
`TestCodexModelListerAsksAConnectionsCLIBeforeTheNextTick`, covering a
just-saved connection and a re-pointed one. It fails on the old code with
the walkthrough's own message.

### 3. Audit / security named the team by id — fixed

Reproduction: as carol, open `/t/alpha/settings/audit`. It shows
'Secrets resolved and Git credentials used by work in team "alpha"'.
This was the one pane 85e06ec18 missed. Fix: `TeamAuditPanel.jsx` uses
`teamLabel(team)`, giving "Alpha Squad (alpha)". Regression test:
`TeamAuditPanel.test.jsx` "names the team by its display name, with the
id after it".

### Smaller observations, not changed

- A browser with no remembered grain username opens the sign-in dialog
  on "New user". Chromium autofills the saved username and password into
  that form, and its button ("Create account") stays disabled for want of
  "Confirm password" until the user picks "Log in". This is the
  documented first-visit behaviour, but a returning user on a new browser
  meets it first.
- The run-time missing-CLI failure is filed as "this run's sandbox could
  not be prepared", with the setup note "giving the sandbox its git
  credentials". The detail after it is accurate.

## Stubs

- **Model provider: claude CLI.** `/tmp/step3/cli-a/claude` and
  `/tmp/step3/cli-b/claude` are `sh` scripts. Each appends
  `<time> $0 model=<--model>` to `/tmp/step3/cli-runs.log`, answers
  `--version` with `2.1.0 (Claude Code)`, and otherwise prints a minimal
  stream-json transcript: `system/init`, one `run_command` tool use and
  its result, and `result/success`. That is the shape of
  `cmd/grain/model_backend_dispatch_test.go`'s Claude stub.
- **Model provider: codex CLI.** `/tmp/step3/codex-{a,b,c}/codex` log
  `$0` and their arguments, answer `--version` with
  `codex-cli 0.153.0` (`codex.ReviewedCLIVersion`), and answer
  `app-server`'s initialize and `model/list` with one model each, like
  `codexCatalogStub` in `cmd/grain/daemon_cli_path_test.go`.
- Credentials are fake (`sk-ant-oat01-stub-a`, `sk-stub-a`, …). Claude's
  discovery went to the real Anthropic API, which refused them with 401.
- Browser: `localStorage["grain.username"]` was set by an init script for
  the non-step-2 contexts, so the first-visit dialog does not cover the
  app. Sessions came from the real `POST /api/auth/login`.
- No GitHub (`-github-host 127.0.0.1:9`). Nothing in grain's API or React
  was mocked or routed.

## Not run

- `cliPath` for Antigravity connections, and remote executors (where the
  executor's own binary wins). The fix applies to the agy lister by the
  same change, but no agy stub was walked.
- Step 2 with Codex's `auth.json` box (`CodexAuthFields`, also
  `new-password`). Only the Claude OAuth-token box was exercised.
- WebKit/Safari autofill. Only Chromium's password manager was used.
- Container-mode `cliPath` (a path inside the daemon's container).

## Tests added

- `pkg/ui/memory_test.go`: `TestListMemoriesShowsAMemberOnlyTheirOwnTeams`
- `cmd/grain/daemon_cli_path_test.go`:
  `TestCodexModelListerAsksAConnectionsCLIBeforeTheNextTick`
- `ui/src/components/TeamAuditPanel.test.jsx`: display-name case

## grain CI

- `72c274c` (the three fixes): every job passed except `ui e2e`, where
  one WebKit test failed: `phone.spec.js` "does not scroll sideways",
  with "Fetch API cannot load …/api/restart-watch due to access control
  checks". That is WebKit reporting the app's restart-watch long-poll
  being cancelled by the test's own `page.goto`. This branch's only
  frontend change is one sentence in `TeamAuditPanel`.
- `632cdc5` (this report): an uncached recheck of `ui e2e`
  (`69f29556c0609846`) passed, 644 passed and 1 skipped, phone test
  included. The full suite then read green (`8c15c5ca09053a50`).
  This last commit only touches this section, which no job's fingerprint
  includes.
