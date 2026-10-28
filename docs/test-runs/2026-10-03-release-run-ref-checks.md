# Test run: trusted release-run ref checks (2026-10-02 plan §1, step 7)

Task: grain/task-1692. Plan: `docs/test-plans/2026-10-02.md` §1, step 7. This follows up
`2026-10-02-rbac-writes.md` (tasks 1573 and 1613). Walked 2026-10-03 UTC.

That report could not reach the release-run checks from `fb4c3e6bf`, "trusted release runs need a
repo admin and a reviewed ref". Ordinary `grain demo` wires no grain CI, so every
`POST /api/repos/{o}/{n}/release-runs` answered **404 `this deployment has no grain CI wired`**
before `checkReleaseRef` ran.

## Environment

- **Code under test:** `main` at `ac2c98d41`, plus this branch (`grain/task-1692`).
- **Toolchain:** Go 1.26.6, Node v20.19.0, Playwright Chromium (chromium-headless-shell-1234).
- **Server:** `grain demo -addr 127.0.0.1:0 -open=false -fake-ci` on its embedded throwaway SQLite store.
- **UI:** the production bundle (`cd ui && npm ci && npm run build` into `pkg/ui/static`).

## Stub recorded

This is the one stub, per the UI-walkthrough rules. Nothing else in the walk is stubbed: not grain's
APIs, not the browser.

| Boundary | Stub | Why |
|---|---|---|
| grain CI (`ui.Config.CI`, the `ui.CIRunner` a daemon fills with `ci.Service`) | `demoFakeCI` in `cmd/grain/demo_fakeci.go`, wired by the new `grain demo -fake-ci` flag. `Start` records the request as a run (`fake-ci-N`) and returns it. The run stays **pending** and nothing executes. `Get`, `List` and `Find` report what was recorded. `Cancel` is the only way a run finishes. `WorkflowOptions` offers nothing, and the cache calls report that no cache exists. | Running a workflow needs a checkout, a sandbox and a runner, and none of that is what this check is about. The observability demo's real `ci.Service` runs on the host, which is the opposite of what an RBAC walk wants. Every check in front of `Start` stays real: the route, `requireRepoAdmin`, `ciEnabledFor` and `checkReleaseRef`. |
| Base-branch reconciler | `-fake-ci` also writes a `base_branch_ci` row for `<default-target-repo> main`. | A real deployment's reconciler writes that row once grain merges into the branch. `checkReleaseRef` reads it as "reviewed". A demo has no reconciler, and no HTTP route may create the row. |

`-fake-ci` and `-observability` each wire a CI, so the demo refuses to take both flags.
`-fake-ci` does not turn grain CI on for the repository. The walk does that itself
(`PUT …/ci-mode {"ciMode":"grain"}`), so the "not enabled" refusal can still be reached.

## Commands

```
cd ui && npm ci && npm run build
NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install chromium   # through the package cache's TLS
npx playwright test --project=chromium e2e/release-run-ref-checks-walkthrough.spec.js
go test ./cmd/grain -run TestFakeCIDemo -count=1
# by hand: go build ./cmd/grain && ./grain demo -addr 127.0.0.1:18431 -open=false -fake-ci, then curl with a login cookie
```

## Setup

All setup was done as the deployment's own actor over loopback, with no session.

- **Users:** registered `repoadmin`, `outsider` and `sysadmin`. `sysadmin` is one of the demo's `AdminAccounts`.
- **Repo admin:** `PUT /api/repos/acme/widgets/admins {"admins":["repoadmin"]}`.

Each check below was made with a real login session (`POST /api/auth/login`).

## Outcomes

| # | Caller | Request | Expected | Seen |
|---|---|---|---|---|
| 1 | outsider | `{"branch":"main"}` | 403, not a repo admin | 403 `user "outsider" does not have permission to modify repository acme/widgets` |
| 2 | repoadmin | `{"branch":"main"}`, before grain CI is on | 403 | 403 `grain CI is not enabled for acme/widgets: attach the "grain-ci" capability …` |
| 3 | repoadmin, through the repo page's **Run release by hand** form | branch `grain/task-9` | 403, unreviewed ref | The banner reads `grain/task-9 is not a branch a release of acme/widgets may run from: name one of main, or ask a system admin` |
| 4 | repoadmin | `{"branch":"main","commit":"0123…4567"}` | 403, pinned SHA | 403 `only a system admin may pin a release run of acme/widgets to a commit: name the branch instead` |
| 5 | repoadmin | `{}` | 400 naming the allowed branches | 400 `branch: name one of main` |
| 6 | repoadmin, through the form | branch `main` | accepted | 202. The CI run dialog opens, and `GET /api/ci/runs` lists one release run: `ref: main`, `requestedBy: repoadmin`, no commit, pending |
| 7 | sysadmin | `{"branch":"grain/task-9","commit":"0123…4567"}` | accepted: a system admin may republish any ref at a pinned commit | 202, `requestedBy: sysadmin` |

After rows 1 to 5, `GET /api/ci/runs` held no release run. None of the refusals recorded anything.
No 500 was seen.

The by-hand curl walk gave the same answers, with `grain/task-9` posted directly to the API in place of the form.

## Coverage

- `ui/e2e/release-run-ref-checks-walkthrough.spec.js` (new) covers rows 1 to 7 against the real demo
  server. It runs in its own demo (`demoArgs: ["-fake-ci"]`). It passed locally in Chromium (1 passed, 9.4s).
- `cmd/grain/demo_test.go` `TestFakeCIDemoAcceptsAReleaseRunAndNeverRunsIt` checks the wiring: the
  watched row exists, a release run of `main` is accepted and stays pending, and `Cancel` finishes it.

## Not covered

- A branch of an open release (`Release.ProdBranch` / `LatestBranch`) as an allowed ref. The demo
  seeds no releases, and creating one goes through GitHub.
