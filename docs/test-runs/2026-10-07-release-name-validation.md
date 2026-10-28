# 2026-10-07: release names git cannot use as a branch (plan §4)

Plan: `docs/test-plans/2026-10-07.md` §4. Task 2423, on `candidate`.

## Environment

- Base: `candidate` at `6634d5c81`.
- Feature commit: `b8c807bef` (`main` `c813631ef`).
  - `git branch -r --contains b8c807bef` lists `origin/candidate` and
    `origin/grain-merge-queue/candidate`, along with several task branches.
- Sandbox: Debian 12 (kernel 6.1), 4 vCPU.
- Tools: Go 1.26.6, git 2.39.5, Node 20.19.0, Playwright 1.62.1 (chromium),
  with `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- UI: `npm ci && npm run build`, so the production bundle in `pkg/ui/static` was used.
- Servers:
  - `go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false` (built as
    `/tmp/grain`), for the pane and `curl` steps.
  - `grain daemon -data-dir /tmp/dd -disable-native-sandboxes -ui-addr 127.0.0.1:45800
    -ui-trust-loopback -github-host 127.0.0.1:18080 -github-insecure-http`, for the
    pre-fix row. It started locally and reconciled every 10 s.
  - A throwaway HTTP wrapper around `pkg/github/githubsim` stood in for GitHub
    (see Stubs). It served a fresh bare repository (`/tmp/bare`, one commit on `main`).
- Pre-fix binary: `grain` built at `b8c807bef^` in `git worktree add /tmp/old b8c807bef^`.

## Steps and results

### 1. Property test against `git check-ref-format --branch`

`pkg/model/release_name_git_test.go`, `TestValidReleaseNameAgreesWithGit`.

- Oracle: a name is valid exactly when all of the following hold:
  - `git check-ref-format --branch` accepts `n`, `n.latest` and `n.rc.1`;
  - `n` does not end in `.latest` or `.rc.<digits>`;
  - `n` starts with a letter or digit. This is grain's own stated rule, and
    the one place it is deliberately stricter than git.
- Corpus:
  - Every one-character string in the charset (65 strings).
  - Every two- and three-character string over one representative of each
    character class: `a`, `Z`, `0`, `9`, `.`, `_` and `-` (392 strings).
  - Targeted cases:
    - `..`, `.lock`, `a.lock`, `v3.lock`, `v3.0..x`, `v3.`, `v3.0-lock`, `a-lock`, `lock`;
    - `a.b.c`, `.latest`, `a.latest`, `a.latest.x`;
    - `.rc.7`, `a.rc.7`, `rc.7`, `a.rc.`, `a.rc.x`, `a.rc.07`;
    - `HEAD`, `head`, `HEAD-`, `HEAD.x`, `FETCH_HEAD`;
    - 63-, 64- and 200-character names, and `_`/`-` runs of 70.
- Why the corpus uses representatives: the literal full 2–3 character set is
  278,850 names and roughly 800k git processes, which took minutes, not
  seconds. Git treats every letter and digit alike except in the words it
  reserves, and those are listed as targeted cases.
- Runtime: about 0.2 s, so the test was kept. It skips when `git` is missing.
- First run, against `candidate` unchanged:
  - **`ValidReleaseName("HEAD") = true`, but git refuses it** (finding 1, fixed).
  - Every name starting with `_` (for example `_`, `__a`, `_-`) is refused by
    grain and accepted by git. This is deliberate (`releaseNameSafe`'s doc
    comment), so the oracle states it explicitly.
  - No other disagreement.
- After the fix: pass. With the fix stashed, it fails on `HEAD`.

### 2. Entry points: pane, `curl` and CLI

Results against the fixed tree, from both the releases pane (Playwright) and
`curl -X POST /api/repos/acme/widgets/releases`:

| name | result |
|---|---|
| `v3.0..x` | 400, rule spelled out |
| `v3.lock` | 400, rule spelled out |
| `v3.` | 400, rule spelled out |
| `v3.0-lock` | **201**, `v3.0-lock.latest` / `v3.0-lock` |
| `HEAD`, `_v3`, `v3.latest` (extra) | 400 |

- Before this change, the refusal was a 400 that said only
  `invalid release name`. That is readable, but nobody can act on it
  (finding 2, fixed).
- The 400 now reads: `invalid release name: use letters, digits, ".", "_" and
  "-", starting with a letter or digit; a name cannot contain "..", end in "."
  or ".lock", be "HEAD", or end in ".latest" or ".rc.<number>"`.
- The pane shows the same text in its error alert, and no request returned a 500.
- New spec `ui/e2e/release_name_validation_walkthrough.spec.js`: 2 passed.
  It covers the pane, and a direct `POST` through Playwright's `request`.
- `team_repo_defaults_release_names.spec.js` and
  `repository_release_memory_walkthrough.spec.js`: 4 passed.
- **CLI**: no `grain` subcommand takes a release name. The subcommands are
  listed in `cmd/grain/main.go` and `grain repo`. `grain repo work-ownership
  -item release` names a work item, not a release. `pkg/ui/releases.go` is the
  only caller of `Store.CreateRelease`, so there was nothing to run.

### 3. A row stored before the fix

1. Started the pre-fix daemon (`/tmp/grain-old`) on `/tmp/dd`.
2. Ran `grain repo add acme/widgets`.
3. Ran `curl -X POST …/releases -d '{"name":"v3.0..x"}'`, which returned
   **201** with `status: provisioning`.
4. Stopped the pre-fix daemon and started the build under test on the same
   data dir.

Fixed tree before the reconciler fix (14:32:50–14:38:00, 31 cycles):

- The GitHub stand-in logged, every 10 s cycle:
  - `GET /repos/acme/widgets` → 200
  - `GET …/branches/main` → 200
  - `GET …/branches/v3.0..x.latest` → 404
  - `POST …/git/refs` → 422 (`update-ref … refusing`)
  - `GET …/branches/v3.0..x.latest` → 404, from the first candidate's cut
- Totals: **154 GitHub calls in five minutes**, about 44,000 a day per row.
- The daemon log mentioned the release **zero** times. The only trace was
  `last_error`, rewritten every cycle (finding 3, fixed).
- The pane and the API showed the row as `provisioning`, with the 422 quoted
  as its error. No 5xx.

Fixed tree after the fix (14:39:53–14:44:53):

- **0 GitHub calls**, and **one** daemon log line:

  ```
  orchestrator: release v3.0..x of acme/widgets: release name "v3.0..x" is not a branch name git accepts, so none of its branches can ever be created; it was stored before grain refused such names, and nothing more is attempted for it
  ```

- The release and its `v3.0..x.rc.1` candidate both show that sentence in the
  pane (captured with Playwright), and no request returned a 5xx.

Removal, against the fixed daemon:

- `DELETE …/releases/v3.0..x` → **405**.
- `POST …/merge` and `POST …/candidates/promote` → 400, "this release is not
  active yet…".
- Creating `v3.0..x` again → 400 for the name.
- The pane has no remove control, so **the row cannot be removed** (finding 4,
  not fixed here; follow-up proposed).
- The cut schedule (`CutCandidate`) refuses any release that is not active, so
  it never touches this row.

### 4. Cut and promote at the charset's edges

`pkg/orchestrator/release_names_test.go`,
`TestSyncReleasesCreatesEveryBranchForNamesAtTheCharsetEdges`.

- Setup: `githubsim` over a throwaway bare repository, with the real
  `github.RESTClient` and an embedded SQLite store.
- Names: `a×63+Z` (64 characters), `20261007`, `a__--__b`, `9_-.-_0`, `x` and
  `v3.0-lock`.
- Sequence: create → sync → `PromoteCandidate` → sync → `CutCandidate` → sync.
- `git ls-remote --heads <bare>` shows `n`, `n.latest`, `n.rc.1` and `n.rc.2`
  for every name. Pass.

## Findings

1. **`HEAD` was accepted as a release name.** Git refuses it as a branch, so it
   could never be provisioned.
   - Reproduction: `go test ./pkg/model -run TestValidReleaseNameAgreesWithGit`
     without the fix. On `6634d5c81`, `ValidReleaseName("HEAD")` returns
     true, and `CreateRelease` checks nothing else about the name.
   - **Fixed**: `ValidReleaseName` refuses `HEAD`. Regression tests: the property
     test, plus `HEAD` in `TestCreateReleaseRejectsAnInvalidName` in both the
     SQLite and the PostgreSQL store suites. The PostgreSQL suite skips in this
     sandbox (no server) and runs in CI.
2. **The refusal did not say why.** The 400 said only `invalid release name`.
   - Reproduction: `curl -X POST …/releases -d '{"name":"v3.0..x"}'` on
     `6634d5c81`.
   - **Fixed**: the message states the rule. The new Playwright spec checks it
     from the pane and from a direct `POST`.
3. **A pre-fix row was retried forever and silently.** Each 10 s cycle made five
   GitHub calls, logged nothing, and rewrote `last_error`.
   - Reproduction: §3 above, or
     `go test ./pkg/orchestrator -run TestSyncReleasesStopsAskingGitHub` without
     the fix, which shows 15 calls in 3 cycles.
   - **Fixed**: `syncRelease`/`syncCandidate` check the stored name first, record
     one clear error on the release and its candidates, log it once (only when
     the stored error changes), and make no GitHub call.
   - The reconciler tests run on the SQLite store only, as the rest of
     `pkg/orchestrator` does. The change touches no store code.
   - `pkg/orchestrator` is a trust-boundary path, so a security-review hold is
     expected.
4. **A release cannot be removed.** There is no `DELETE` route and no pane
   control, and merge and promote both need an active release. So a pre-fix
   `v3.0..x` row stays in the pane for good. The 2026-10-06 report noted this
   as an observation but did not file it.
   - Reproduction: §3 above.
   - **Not fixed here**: it needs a design decision (delete, or an "abandoned"
     status) across the API, both stores and the pane. Proposed as a separate
     task against `main`.

5. **Test coupling, found by this branch's first CI run.**
   `repository_release_memory_walkthrough.spec.js` drove `releasehelper` without
   `-release`, so the helper acted on the oldest release in `acme/widgets`. Once
   the new spec had created `v3.0-lock` in the same worker's store, the helper
   marked that release's candidate instead, and "Simulated remote git branch
   failure" never appeared.
   - Reproduction: `npx playwright test e2e/release_name_validation_walkthrough.spec.js
     e2e/repository_release_memory_walkthrough.spec.js --project=chromium
     --workers=1`.
   - **Fixed**: the walkthrough's three helper calls name its own release. The
     same command now passes 4/4.

Observation, not filed: grain refuses a leading `_` that git would accept. That
rule is documented and deliberate.

## Stubs

- **GitHub**: `githubsim.Sim` over a real bare repository, in the orchestrator
  tests and behind the throwaway HTTP wrapper for the local daemon. The wrapper
  was `/tmp/old/cmd/simserver`, about 40 lines and not committed.
- **Agent backends, sandboxes and the package cache**: none configured. The
  daemon ran with `-disable-native-sandboxes`, and no agent ran.
- Nothing in grain's own API or React was mocked.

## CI verdict

- First full run, `eea19570ca5425df` at `8efa46b`: `ui e2e` red (finding 5);
  every other job finished green.
- Second full run, `91d9354b1d5a7922` at `a903fd7`: **green**. All of frontend,
  go, installer, lint, package cache, postgres, terraform and ui e2e passed.
  The postgres job runs the PostgreSQL store suite, including the `HEAD` case.
- No known flakes recurred.
- This report's own commit was re-checked with a full `wait_for_checks` before
  the branch was finished.
