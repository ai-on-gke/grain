# Walkthrough: `grain demo -config-change` (test plan 2026-11-09 §3)

Task 3730, filed from [`docs/test-plans/2026-11-09.md`](../test-plans/2026-11-09.md) §3 (task 3713).
Subject: PR #968 (`599ce654`, `pkg/demo/demo_config_change.go`). The demo serves a real throwaway `grain/config` and parks two tasks on real proposal branches: raise `poll_interval_ms` from 10 s to 30 s, and set a prompt extension.

## Environment

- Branch `grain/098073e4/task-3730-walkthrough-grain-demo-config`, built from `4d7d6373` (`origin/main`): `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`.
- Debian sandbox, Go 1.26.10, Node 20.19.0, git 2.39.5, Playwright Chromium (headless shell build 1234, from `/var/cache/playwright`).
- Demos: `grain demo -config-change -github-sim -fake-ci -git-proxy -addr 127.0.0.1:429x`, one fresh demo per scenario, plus `-secrets-store -reconcile 2s` for item 4 and `-as viewer@example.com` for item 3. Every API call and the harness sign in with the demo's access code (`<dir>/local-api-token`).
- Harnesses, in [`2026-11-09-demo-config-change/`](2026-11-09-demo-config-change/):
  - `walk.mjs` drives the card in Chromium. `STEP=layout|viewer|approve|reject|both|approve-stale`; it prints JSON lines, which are kept beside it as `*.jsonl`.
  - `flags.sh` covers item 4. `flags-before.txt` is its output on `main`'s binary, `flags-after.txt` on this branch's.

## Steps and results

### Item 1: approve changes the running demo

`STEP=approve` (`approve.jsonl`):

| | before | after Approve & Apply |
|---|---|---|
| `GET /api/settings` `pollInterval` | `10s` | `30s` |
| `grain/config` `main` head | `9605b86` (initial export) | `dec89ab`, a `--no-ff` merge whose second parent is the proposed head `0ae7d18` |
| `tables/grain_config.json` on main | `"poll_interval_ms": 10000` | `"poll_interval_ms": 30000` |
| `grain/config/20/poll-interval` | present | deleted |

- `POST …/config-change/approve` answered 200, the card left, and "Approved and applied config change (0ae7d187f9b2)" was posted.
- After a reload the card stays gone, and `pendingConfigChange` is cleared.
- The change applies live (the next sync imports it), as on a daemon. So no **Pending restart** chip appears, and `/api/state-repo` has no pending-restart field: `poll_interval_ms` needs no restart.
- The demo log shows the daemon's own import messages: the settings snapshot, and "poll_interval_ms was 10000 and is now 30000".

### Item 2: reject, both, conflicts

- **Reject** (`reject.jsonl`):
  - 200. The branch `grain/config/21/prompt-extension` is deleted, `main` is untouched (`"prompt_extension": ""`), and the task returns to `queued` with "Rejected config change (9a7576e09fa7): Not every repository has unit tests" as its last comment.
  - That matches `StateManager.DiscardConfigChange`: the branch goes, and the comment wakes the session.
- **Both, poll first** (`both1.jsonl`, with `-reconcile 2s`):
  - Both answer 200, and `main` ends with `poll_interval_ms: 30000` and `prompt_extension: "Run the unit tests before you push."`.
  - `/api/settings` reports both live.
  - `-reconcile` exported an "Update grain state" commit between the two merges (unrelated rows: acme/widgets' declared templates, which the demo's reconcile drops at start-up). The second merge still went in cleanly.
- **Both, prompt first** (`both2.jsonl`, fresh demo): both 200, and both edits land.
- **Conflicting edit on main** (`stale.jsonl`, fresh demo):
  - A sandbox cannot push `main` through the proxy (see below), so the conflicting edit was an operator's: `PUT /api/settings {"pollInterval":"20s"}`. That was exported to `grain/config` as `c38158f Update grain state after a write by the access code`, setting `"poll_interval_ms": 20000` on the very line the proposal edits.
  - Approve & Apply then answered **422**: "the config change does not pass validation: the change conflicts with the current main: rebase it onto origin/main and propose it again".
  - `main`, the branch and the setting (20 s) are untouched, and the error shows on the page. **No silent overwrite.**
  - See finding 4 for what the card goes on saying.

### Item 3: role gating

- The decision routes need `settings:write` (`pkg/ui/access_roles.go`, `humanRouteScopes`). `route_scopes.go` marks them `internal`, so no agent scope reaches them.
- **On the route:**
  - In the demo, an agent's call (`X-Grain-Acting-Task: 21`) gets **403** "… is not part of the agent API" for both approve and reject, and a call with no credential gets 401.
  - A verified grain/viewer can't be produced in a demo: its access code always has full access, and `-as` only attributes the seeded tasks (`GET /api/config` `access` is `null` with `-as viewer@example.com`). So the viewer's 403 is pinned in `pkg/ui` instead (`TestViewerRoleIsRefusedConfigChangeDecisions`): both routes answer 403 naming `settings:write` and the role.
- **In the UI:** a grain/viewer page (`GET /api/config`'s `access` replaced, as `role-gated-actions.spec.js` does) was offered both buttons enabled (`viewer-before.jsonl`). That is **finding 2**, fixed: both buttons are now disabled inside `AccessGate` with "Needs settings:write, which your role grain/viewer does not grant." (`viewer-after.jsonl`).

### Item 4: flag edge cases (`flags.sh`)

- `-config-change -external-config-repo …` exits **2** with "grain demo: -config-change and -external-config-repo are mutually exclusive: an external config repository takes no proposals here".
- `-config-change -github-sim -fake-ci -git-proxy -secrets-store -reconcile 2s` starts and serves.
- On SIGINT (exit 130) and SIGTERM (exit 143), the process, the UI port and the git proxy port all go.
- The throwaway directory did **not** go on `main` (`flags-before.txt`: `FAIL … throwaway dir removed` for both signals). That is **finding 3**, fixed (`flags-after.txt`: all PASS).

### Item 5: visual check

- `make ui-screenshots`'s script (`ui/e2e/screenshot.mjs --url`) shot `/tasks/20` and `/tasks/21` at desktop (1440) and phone (390), light and dark, before and after, plus `/tasks/20` as a viewer.
- This sandbox's file reader shows PNGs as bytes, and `LOOK=1` has no Gemini key here, so the shots were checked by measurement (`STEP=layout`, `layout-*.jsonl`) and are attached to the task for a person to look at:
  - At both widths and in both themes the page does not scroll sideways (`scrollWidth` = viewport: 1440/1440, 390/390).
  - No element outside the diff extends past the card's right edge.
  - The diff fits the card (scroll width = client width: 831 px at desktop, 325 px at phone).
- The decision buttons sit at the card's bottom-left (Approve & Apply 16 px and Reject 157 px from its left edge). The plan's "right-hand slot" does not describe this card, which has always put them bottom-left in a row of their own. Gated, they keep exactly the same offsets.

### Git proxy

- `POST /api/e2e/git-proxy/runs` hands a `grain/config` task the remote `<proxy>/grain/config.git`. On `main`, a clone through it got **404** "GitHub answered 404 …": the demo forwarded `grain/config` to the simulated GitHub. That is **finding 1**, fixed.
- With the fix, a clone through the proxy shows `main` and both proposal branches at the commits `/api/state-repo` and the checkout report, and `git fetch` after approval sees the merges.
- The proxy's rules still hold: a sandbox's push to `main` is refused ("A change reaches grain/config's main only through propose_config_change …"). A push to `grain/config/22/probe` from a task without `grain/config` in its writes is refused too.

## Findings

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | The demo's git proxy didn't serve `-config-change`'s `grain/config`: `pkg/demo/demo_gitproxy.go` wired no `gitproxy.LocalRepository`, though the daemon does (`pkg/daemon/servers.go`). Clones returned 404. | Medium (demo only) | Fixed (`38e63d2a`). Regression: `config-change-card.spec.js` "the demo's git proxy serves grain/config as a session reaches it". Fails before, passes after. |
| 2 | The config-change card offered Approve & Apply and Reject to roles without `settings:write`. The server refuses them, but the UI gave no sign. | Medium | Fixed (`6328c629`): both buttons in `AccessGate scope="settings:write"`. Regressions: `PendingConfigChange.test.jsx` (vitest), `config-change-card.spec.js` "a viewer is shown the change but offered neither decision" (fails before, passes after), and `TestViewerRoleIsRefusedConfigChangeDecisions` for the route. |
| 3 | `grain demo` never removed its `/tmp/grain-demo-*` directory (store, secrets key, `grain/config`) on SIGINT/SIGTERM. True of every demo mode, not just `-config-change`. | Low | Fixed (`ce84b2ab`): removed on either signal, which is then re-raised so exit statuses are unchanged. A signal inherited as ignored stays ignored. The first attempt re-raised a SIGINT the background demo was ignoring, which removed the directory under a demo that kept serving; it was caught by this walkthrough and fixed before commit. Reproduction: `flags.sh`. |
| 4 | After a conflict refusal (422), the card still shows its proposal-time validation, "Schema valid · … · Dry-run apply succeeded", beside the error, and still offers Approve & Apply. That holds even after a reload. The only way forward is Reject with a reason asking for a rebase. | Low (UX) | Not fixed: changing it means re-validating the stored proposal on read or on refusal (`pkg/ui/task_config_change.go`), which changes product behaviour rather than fixing the demo. Proposed as a follow-up. |
| 5 | On the first approval, the demo logs "this deployment held 3 settings rows that had never been exported … settings/suites.yml (id = 1..3)", though `settings/suites.yml` is in the initial export. Caused by how the demo seeds (`staterepo.Load`, then `StateManager.Sync`); it didn't affect any outcome. | Cosmetic (demo log) | Recorded only. |

Also observed, not a defect: `POST /api/e2e/git-proxy/runs` for a seeded task starts a run on it, so the task reads `running` afterwards. The walkthrough minted tokens for a separate `grain/config` task to keep the seeded ones parked.

## CI

See the pull request's checks for the branch (`grain ci wait`). Locally:
- `go test ./pkg/ui -run TestViewerRoleIsRefusedConfigChangeDecisions`, `go test ./pkg/demo -run ConfigChange` and `go vet ./pkg/demo` pass.
- `npx vitest run src/components/detail/PendingConfigChange.test.jsx` passes (5 tests).
- `npx playwright test e2e/config-change-card.spec.js` passes (4 tests); 2 of them fail on the code without the fixes.
- No `grain demo` flag changed, so the CLI spec needed no regeneration.
