# Test run — GitHub issue importer (2026-10-31)

Plan: `docs/test-plans/2026-10-30.md` §1 (per-repository import settings,
"Imported" tasks, *Import issues now*; PR #194, `e93fdcae`, `a0a64bde`,
`5b46752c`, `7130d12f`). Task 2010, filed by the nightly test-plan job
(task 1899).

## Environment

- Base: `origin/main` at `01fc11c2` (Merge PR #309). Fixes on
  `grain/049f1e09/task-2010`.
- Build: `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`
  (go1.26.10, node v20.19.0).
- Demo: `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:8411`
  (later also `-scheduled-workflows` for step 4's declarations). The
  walkthrough spec runs `-github-sim -fake-ci` with no `-reconcile`, so
  only *Import issues now* imports and the counts are deterministic.
- Daemon (step 5): `grain init workstation -non-interactive -repo acme/widgets
  -sandbox-backend host -model-backend claude -github-token-file … -ui-addr
  127.0.0.1:8422 -start-service=false`, then `~/.grain/daemon.sh
  -sandbox-port-base=0` under a throwaway `HOME`, as
  `tests/e2e/standalone_local_merge_test.go` does, but **without** its
  `DirectConfigWrites: true`. grain/config in internal mode (`GET
  /api/state-repo`: `mode: internal`, schema 77).
- Browser: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`,
  chromium-1234), 1440×900 and 390×844, light and dark.
- External boundaries stubbed: GitHub only, by `-github-sim` (and, for the
  daemon, no GitHub at all: only the config write path was exercised there).

## Findings

| # | Finding | Severity | Outcome |
|---|---------|----------|---------|
| F1 | On `grain demo -github-sim`, *Import issues now* answered **"this deployment cannot import GitHub issues: it has no GitHub client"** (400). The demo never wired `ui.Config.IssueImporter` over the sim (the daemon does, in `pkg/daemon/ui_server.go`), and `-reconcile` never ran the periodic `issue-imports` pass. | Bug (demo) | **Fixed**: both wired (`pkg/demo/demo_githubsim.go`, `demo_reconcile.go`). Regression: `TestDemoGitHubSimImportsIssues`, `ui/e2e/issue-import-walkthrough.spec.js`. |
| F2 | Ticking *Import open GitHub issues* saves at once, and the first periodic pass runs on the next cycle. With `-reconcile 2s`, ticking the switch and then typing the label imported the **whole unlabelled backlog** (#1, #8001, #8002) before the label was saved. A daemon behaves the same way (the first pass runs on the next cycle after enabling). | UX flaw | **Fixed for the "type first, then tick" order**: switching on now saves the typed source, label and interval in the same PUT (`RepoPage.jsx`). Regression: `RepoPage.test.jsx` "switching issue import on saves the label typed before it", and the spec asserts the PUT body. Ticking first and typing afterwards still imports at once. Filed as a follow-up (hold the first periodic pass for one interval, or confirm when there is no label). |
| F3 | `githubsim`'s `GET /repos/{o}/{n}/issues` ignored `state` (it listed closed issues) and listed no pull requests. Real GitHub does the opposite on both counts, so neither the importer's open-only read nor `ListIssues`' `pull_request` skip had ever run against it. | Sim fidelity | **Fixed**: honours `state` (default `open`, `closed`, `all`), lists PRs with a `pull_request` key, and takes `PATCH …/issues/N {"state"}`. Regression: `TestSimListIssuesIsGitHubsListing`. |
| F4 | An upstream source on the demo (`issueImportRepo` = any other `owner/name`) sent the request to a `Sim` configured for `acme/widgets`, and `mustOwn` **panics**. Nothing in the demo could show an upstream import. | Bug (demo) | **Fixed**: the demo's transport routes `/repos/{o}/{n}` to a second simulated repository, `acme-upstream/widgets` (one open issue, #7), and answers GitHub's 404 for any other repository. A loopback-only `/api/e2e/github-sim/repos/…` hook lets a walkthrough open, label and close issues. Documented in `docs/github-issue-import.md`. |
| F5 | Plan drift: §1 gap 5 expects every internal-mode daemon to refuse the save with `config_managed_externally`. On current `main`, internal mode refuses non-bootstrap writes **only when `grain_config.settings_locked` is on** (`configRepoFor`, `pkg/ui/config_refusal.go`). The default is off, and then the PUT writes directly (200). | Plan out of date | Not a bug. Walked both ways (below). |

No other defects found.

## Steps and outcomes

### Steps 1–2: real import from the Agents tab (gap 1)

Seeded through the new hook: #8001 "Add a dark-mode toggle"
(`grain-agent`), #8002 "Document the CLI flags" (no label), #8003 (`grain-agent`,
then closed), and PR #9000 on branch `feature/readme`, labelled `grain-agent`.
The demo's own seed is #1 (`grain-agent`). The sim's listing with
`labels=grain-agent`: `#1 open`, `#8001 open`, `#9000 open (pull_request)`.

- Before F1's fix: both presses showed
  "this deployment cannot import GitHub issues: it has no GitHub client".
- After (spec, no `-reconcile`): label `grain-agent` typed, switch ticked →
  `PUT …/settings {"issueImportEnabled":true,"issueImportRepo":"","issueImportLabel":"grain-agent","issueImportIntervalSeconds":0}`.
  First press: **"Read 2 open issues on acme/widgets: imported 2, 0 already imported."**
  Second press: **"Read 2 open issues on acme/widgets: imported 0, 2 already imported."**
  #8002 (unlabelled), #8003 (closed) and PR #9000 were not imported.
- `/?f=origin:any:imported`: rows `[#42]` (the demo's seeded import), `[#1]`,
  `[#8001]`, each with the *Imported* status and the `imported` chip
  (tooltip "imported from GitHub issue acme/widgets#8001"). `/inbox` lists
  them. The Board card carries the chip. Horizontal overflow was 0 at all
  four variants.

### Step 3: approval, close, dedup, late label (gap 2)

- *Approve* in the task pane: `GET /api/tasks/21` → `state: queued`,
  `awaitingImportApproval` unset, `issue: 8001`. The row loses *Imported*
  and keeps the chip.
- `POST /api/tasks/20/close` (the #1 import) → closed.
- `POST /api/e2e/github-sim/repos/acme/widgets/issues/8002/labels {"labels":["grain-agent"]}`.
- Next pass: `{"open":3,"imported":["22"],"skipped":2}`, i.e. "Read 3
  open issues …: imported 1, 2 already imported." #1 was **not**
  re-imported. #8002 was picked up by its late label. The store holds
  exactly one task per origin.

### Step 4: upstream source (gap 3), declared settings (gap 4)

- Source `acme-upstream/widgets`, no label, saved from the form: "Read 1
  open issue on acme-upstream/widgets: imported 1, 0 already imported."
  `GET /api/tasks/23`: title `[acme-upstream/widgets#7] Widgets render blank on narrow screens`,
  `issue: null`, `origin: acme-upstream/widgets#7`, `awaitingImportApproval: true`.
  The body starts "Imported from GitHub issue acme-upstream/widgets#7:
  https://github.example/acme-upstream/widgets/issues/7", followed by
  author, labels and the description.
- Not reachable in the demo, because agents never run there (`-reconcile`'s
  help says so) and the demo runs no work-tree reconciler. Pinned instead
  by `TestUpstreamImportSaysNothingUpstreamAndFixesNothing`: no progress
  comment is posted upstream while the task is unapproved, and
  `pullRequestBody(…, task.Issue)` carries no `Fixes #`. After approval, a
  progress comment *is* posted on the origin issue, upstream included,
  which is what `docs/github-issue-import.md` describes.
- `.grain/settings.yml` declaring `issue-import: {enabled: true, approved: true, label: grain}`,
  committed to the sim's `main`, with `-scheduled-workflows` and
  declarations switched on for the repo: the declarations read fails with
  ".grain/settings.yml: issue-import: a repository says what its own work
  does, never what that work is allowed to do: …" (the Authority reason).
  `issueImport` stays `enabled: false`. The reason is shown on Repo →
  Advanced ("Last read failed: …") at 1440/390, light/dark, with no
  overflow. Pinned by `TestDeclaredIssueImportIsRefusedAsAuthority`.

### Step 5: internal-mode config write path (gap 5)

- Default daemon (`settings_locked` off): `PUT /api/repos/acme/widgets/settings
  {"issueImportEnabled":true,"issueImportLabel":"grain-agent"}` → 200,
  written directly. `merge-on-green`, `promptExtension` and `declarations`
  behave the same (F5).
- `PUT /api/settings {"settingsLocked":true}`, then the same save → **409
  `config_managed_externally`**, `internal: true`, `repo: grain/config`,
  with `change[0]` a patch to `tables/repo_config.json`
  (`issue_import_label` `grain-agent`→`bug`, `issue_import_approved` 0→1).
- UI with the lock on: the Agents tab renders the import section read-only
  (switch, queue box, fields and Save disabled). Each control's
  `aria-describedby` reads "acme/widgets's settings live in the config repo
  and are read-only here. Change them with a config pull request, or ask
  the Director to." with *View in config repo* links to
  `tables/repo_config.json`. The failure is not silent: the UI never sends
  the PUT, so `ConfigRefusal.jsx`'s dialog (which answers a 409) has
  nothing to show here. *Import issues now* stays enabled, since it is an
  action and not configuration.
- `POST /api/state-repo/pull-requests {"changes": <the refusal's change>}`
  → 201, task 2 on grain/config, validated (`repo_config: 1`).
  `POST /api/tasks/2/submit` → merged into grain/config (`8bbebb1 Merge
  branch 'grain/task-2'`). `GET …/settings` then shows `label: bug,
  approved: true`: **the settings arrived**. Whether the daemon then
  imports was not tested, because this daemon has no GitHub, simulated or
  real.

### Gap 6: queue-at-once

The trust sentence ("Turn it on only if you trust everybody who can open
an issue on acme-upstream/widgets: they write what the agent is told to
do.") is visible beside the box before it is ticked. Ticking saves
`issueImportApproved: true`. A new upstream issue imported after that has
`state: queued` with no `awaitingImportApproval`: it is ready to dispatch
without a person. The demo never runs agents, so "dispatches" stops at
queued.

### Step 6: the spec

`ui/e2e/issue-import-walkthrough.spec.js` walks steps 2–4 and gap 6
end to end (1440 px, then the section's status line and button inside
the section at 390 px, with no page overflow). It passed 3/3 with
`--repeat-each=3`. `ui/e2e/issue-import-layout.spec.js` still passes.

## Screenshots

In `docs/test-runs/2026-10-31-github-issue-import/`:
`before-contact-sheet.png` (Agents → GitHub issue import on the base
build, 1440/390 × light/dark) and `contact-sheet.png` (after: the section
at four variants, the Origin = Imported issue list, the board at 390
dark, the declared-settings refusal on Advanced, and the locked daemon's
read-only section at 390 dark), plus the individual shots. F2 changes
behaviour only, not layout: the section's before and after differ only in
its state (off vs. on with a label and outcome line). Layout was checked
with bounding boxes and `scrollWidth − clientWidth` (0 everywhere)
rather than by eye, because this sandbox has no image viewer and no
Gemini key for `LOOK=1`.

## Follow-ups

- The first periodic import after switching on runs on the next cycle,
  whatever the label (F2, remaining half). Proposed: hold the first
  periodic pass for one interval after enabling, so *Import issues now* is
  the only immediate pass, or confirm before switching on with no label.
