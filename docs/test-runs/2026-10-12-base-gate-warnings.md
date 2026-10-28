# Base-branch gate warnings on Repo overview and Releases (2026-10-12)

Walkthrough of `docs/test-plans/2026-10-11.md` §10 against `candidate`, task 3146 (filed by task 2955).
It covers `7b93861a0`, which took unjudged base-branch gates out of the Inbox and the rail's count and
put them on the repository overview (`RepoPage.jsx`) and Releases (`RepoReleases.jsx`).

## Environment

- Base: `candidate` at `0547af051`; `git branch -r --contains 7b93861a0` lists `origin/candidate`.
- Server: the real `grain demo -github-sim` (`-addr 127.0.0.1:0 -open=false`) on its embedded SQLite
  store, started per worker by `ui/e2e/fixtures.js`; the frontend built with `cd ui && npm ci && npm run build`.
- Browser: Playwright 1.x Chromium headless shell (installed with `NODE_EXTRA_CA_CERTS` pointing at
  grain's CA, since the package-cache proxy re-signs the Playwright CDN).
- Admin: the demo's signed-in admin, with `X-Grain-Admin-Mode: on` (the suite default).
- Read-only user: `vic`, registered through `/api/auth/register`, viewer on a new team `widgets` whose
  `targetRepos` is `acme/widgets` (admin as the team's required admin).

### Stubs

- None of grain's APIs or React were mocked. GitHub is the demo's own simulated forge (`-github-sim`).
- Vitest regressions stub `api.js` and `Element.prototype.scrollIntoView` (jsdom has none), as the
  surrounding tests already do; the notification test uses the existing `FakeNotification`.

## Producing an unjudged gate

The plan suggests red base CI. That does not produce one: a red base branch is a *judged* branch, and
`RepoPage`/`RepoReleases` only draw gates with `judged: false`. Of the unjudged findings in
`pkg/ui/basebranchgate.go`, `judged-nothing` needs an errored run two hours old, so the one a demo can
produce in real time is `grain-ci-off`: the repository's default branch is watched while its CI Mode is
GitHub's, so grain CI never enrols it.

    PUT /api/repos/acme/widgets/ci-mode              {"ciMode":"github"}
    PUT /api/repos/acme/widgets/watch-default-branch {"watchDefaultBranch":true}
    GET /api/base-branch-gates -> [{"repo":"acme/widgets","judged":false,"reason":"grain-ci-off",...}]

## Results

| Step | Who | Observed (before fixes) | After fixes |
| --- | --- | --- | --- |
| Inbox rows / rail chip | admin | 4 rows, chip `4`; no gate row or text | same |
| Repo overview | admin | Two copies of the warning: one in the page header, rendered **lowercase**, and the release pane's own further down. Neither had a link (`View CI run` only appears for a run id) | Header copy keeps its case and has **Go to CI Mode**, which scrolls to the switch |
| Releases | admin | Warning present, no action | Warning has **Open repository settings** (back to the overview) |
| Warning text | all | Advised "Attaching the grain-ci capability ... is what enrols the branch", and read "acme/widgets's base branch its default branch" | Names CI Mode "Use Grain CI", and reads "acme/widgets's default branch" |
| Judge (CI Mode → Use Grain CI) | admin | API cleared at once; header warning stayed **60.4 s** (the shell's `GATE_INTERVAL_MS` poll); release pane's warning stayed until reload | Both clear on the save (asserted within 10 s; in practice immediately) |
| Inbox / chip after judging | admin | unchanged, no gate | same |
| Inbox / chip | vic (viewer) | 0 rows, chip `Inbox`; no gate | same |
| Repo overview / Releases | vic | Both warnings and the Releases warning shown, read-only user sees the finding | same, with the links |
| Judge control | vic | CI Mode radio **offered enabled**; clicking it sent `PUT /ci-mode` → **403**, the page showed `user "vic" does not have permission to modify repository acme/widgets`; the radio stayed on GitHub and the gate stayed | unchanged (follow-up filed) |

## Findings

1. **Overview warning lowercased** (fixed). The new alert sits in `.repo-page-header`, which `style.css`
   puts in the app's lowercase heading set, so it read "nothing is testing ... grain ci is not enabled".
2. **Warning named a repair nobody can make** (fixed). The `grain-ci-off` detail told the reader to attach
   the `grain-ci` capability. Every picker refuses that (`grainci.Refusal`); it is not even in
   `availableCICapabilities`. `ciNotEnabledMessage` (`pkg/ui/ci.go`) had already been corrected for the
   same advice; the gate's sentence was missed. It also read "base branch its default branch".
3. **No link from either warning to the fix** (fixed). The plan's "follow the warning's link" had nothing
   to follow for this finding: Releases' alert has no action at all, and the overview's only appears for a
   gate carrying a run id.
4. **Warning outlives the judge** (fixed). Saving CI Mode refreshed config but not the gates, so the
   overview warning stayed until the next 60 s poll, and the release pane (which reads its own copy off
   `GET /api/repos/{repo}/release`) until reload.
5. **Gate notifications open an Inbox that no longer lists gates** (fixed). `inboxNotifications.js` still
   announces gates ("acme/widgets is not being tested") and clicking one opened the Inbox.
   It now opens the repository. The burst summary ("N new items in your inbox") can still count a gate; left as is.
6. **Read-only users are offered the judge** (follow-up). `RepoPage` shows CI Mode (and its other repo
   settings) enabled to a viewer of the owning team; the server refuses with 403 and the page says so.
   This is page-wide, not specific to gates.
7. **Overview shows the gate twice** (follow-up, design call): the header copy added by `7b93861a0` and the
   release pane's existing `baseBranchGate` alert, on one page.
8. Plan note: "red base CI" does not produce an unjudged gate (see above); `grain-ci-off` does.

## Deterministic reproductions

All on `grain demo -github-sim`, signed in as admin, after the two `PUT`s above:

1. Open `/repos/acme/widgets`: `.repo-page-header .MuiAlert-root` has `innerText` starting
   "nothing is testing" (computed `text-transform: lowercase`).
2. `GET /api/base-branch-gates`: `detail` contains "Attaching the grain-ci capability" and
   "base branch its default branch"; `GET /api/repos/acme/widgets/ci-capabilities` →
   `availableCICapabilities` has no `grain-ci`.
3. Open `/repos/acme/widgets/releases`: the gate alert contains no button. The overview's alert has no button
   (the gate has no `runID`).
4. On the overview, click "Make grain CI the gate for acme/widgets...": `GET /api/base-branch-gates` is
   `[]` at once, the header alert is still there 2 s later and leaves after ~60 s; the release pane's alert
   remains after that.
5. `inboxEntries([], [{repo:"acme/widgets"}])` passed to `announce(...)`, clicking the notification calls
   `onOpenInbox` (vitest).
6. As `vic` (viewer of the `widgets` team): open `/repos/acme/widgets`, click the same radio → `PUT
   /api/repos/acme/widgets/ci-mode` 403.

## Fixes and regression tests

- `pkg/ui/basebranchgate.go`: the `grain-ci-off` detail names CI Mode "Use Grain CI" and says the
  capability cannot be attached by hand; "acme/widgets's default branch" when no branch is resolved.
  `TestSandboxHealthSaysWhenNothingWillEverJudgeAWatchedBranch` asserts both and the absence of the old wording.
- `RepoPage.jsx`: the header warning has `text-transform: none` and a **Go to CI Mode** action for
  `grain-ci-off`; saving CI Mode, "ignore GitHub CI" or watch-default-branch calls the shell's new
  `onRefreshGates` (`App.jsx` → `refreshBaseBranchGates`) and bumps `refreshKey` on
  `RepoReleaseWorkflowSettings`, which re-reads its gate. Vitest: "points an unjudged gate's warning at CI
  Mode and clears it as soon as CI Mode is saved".
- `RepoReleases.jsx`: **Open repository settings** action. Vitest: "links an unjudged gate's warning to
  the repository's settings".
- `inboxNotifications.js`: a gate entry carries its repo and its click opens that repository
  (`onOpenRepo`, wired in `App.jsx`). Vitest: "opens a gate's repository rather than the inbox...".
- Durable e2e: `ui/e2e/base-branch-gate-warnings-walkthrough.spec.js` (own server via
  `demoArgs: ["-github-sim", "-open=false"]`): the admin pass (Inbox/chip exclude the gate, both pages show
  it with working links, judging clears it within 10 s) and the viewer pass (same visibility, the judge
  refused 403 with the server's sentence shown, gate unchanged).

## Follow-ups filed

- Read-only team members are offered repository settings controls on `RepoPage` that the server refuses.
- The overview draws the same gate twice (header and release pane).

## CI verdict

grain CI run `2ff66a129ce620a7` on `34bf553` (the fixes, spec and this report before this section was
filled in): **success**: frontend, go, installer, lint, package cache, postgres, relay image, terraform
and ui e2e all passed. The commit adding this paragraph changes only this file and was re-checked with the
full suite before the run ended.
