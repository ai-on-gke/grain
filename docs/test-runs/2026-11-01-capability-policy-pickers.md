# 2026-11-01: Capability policy at filing and in grant pickers (test plan §3)

Task 2012 (`grain/049f1e09/task-2012`), walking §3 of
[`docs/test-plans/2026-10-30.md`](../test-plans/2026-10-30.md): policy verdicts in
grant pickers and at filing time, `requires_person` approval refusal,
repo-limited scope sets and the scope-set removal confirm (PRs #130
`69ada067`, #173 `971dd9a9`/`00e72c2b`/`9cffcc4e`, #175 `ec12bfe7`/`fe45fb04`,
#141 `16e070e1`, #193 `3d27246f`).

## Environment

- **Tree**: `main` at `7c652762`, merged into this branch, plus this branch's
  fixes for the "after" half.
- **Build**: `make frontend` (then `cd ui && npm ci` for Playwright, and
  `npm run build` after each UI change) and `go build -o /home/debian/grain
  ./cmd/grain` (Go 1.26, Node 20.19).
- **Demo**: `grain demo -github-sim -fake-ci -reconcile 2s -addr
  127.0.0.1:18731 -open=false`.
- **Daemon** (step 5 / gap 6): a real `grain daemon` set up by `grain init
  workstation -non-interactive -sandbox-backend host -model-backend claude
  -ui-addr 127.0.0.1:4300 -start-service=false` under a throwaway `HOME`,
  started from its `daemon.sh` with `-sandbox-port-base=0`. `GET
  /api/state-repo` reports `mode: internal`, `repository: grain/config`.
  Settings were locked (`PUT /api/settings {"settingsLocked":true}`), the
  precondition the §2 walker found the config-write path now needs. The
  demo's capability server (`capabilities.demo.grain.example/cloud`) was
  attached to the daemon by its URL and bearer token before the lock, so the
  daemon's pickers had methods to judge.
- **Callers**:
  - a person: the access code as a bearer token, and a signed-in browser session;
  - an agent: the access code plus `X-Grain-Acting-Task: <id>` and
    `X-Grain-Acting-Reason`, the headers the sandbox API proxy sets, so the
    call goes through `authorizeAgentCall` exactly as a run's would.
- **Browser**: Chromium from `@playwright/test` 1.62.1
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless, at 1440×900
  and 390×844, in light and dark (the `grain.themeMode` key plus
  `colorScheme`). An ad hoc walk script (outside the repo) drove all five
  pickers. The parts worth keeping are now
  `ui/e2e/capability-policy-pickers.spec.js`.
- **Stubbed**: nothing in grain. The demo's `githubsim` and fake CI are its
  own. The demo dispatches nothing and the daemon has no agent backend, so no
  run reached mint. Mint-time refusal rests on the Go test named under gap 2.

## Policy used

Saved on the demo with `PUT /api/capability-policy`, the catalogue in the
same body (its JSON keys are `inProcess`/`requiresPerson`; the YAML spelling
`in_process` is dropped without an error):

| # | Rule | Kind of rule |
|---|---|---|
| 1 | deny `cloud#cloud-viewer`, repos `acme/widgets`, run kinds `task` | deny |
| 2 | allow `cloud#cloud-key`, repos `acme/gadgets` | repo-conditional |
| 3 | deny `cloud#cloud-viewer`, repos `acme/gadgets`, run kinds `task` (added after a task holding it was filed) | deny, saved over a holder |
| catalogue | `gemini-key: {restrictions: {requiresPerson: true}}` | `requires_person` |

## Outcome

| Gap | Check | Outcome |
|---|---|---|
| 1 | New task picker (task, `acme/widgets`) | Pass |
| 1 | Template editor (task, its repo) | Pass |
| 1 | Schedule editor (task, its repo) | Pass |
| 1 | Task pane "Add grant…" (task, the task's repo) | **Bug B, fixed**: a repo-conditional rule left a method admitted on that repo flagged "may be refused" |
| 1 | Repo default grants (task, the page's repo) | Pass |
| 1 | Repo CI grants (ci) | **Bug A, fixed**: not judged for the page's repo |
| 1 | Refusal text inside the popover on a phone (bounding boxes) | Pass: 36/36 disabled rows before the fixes, 40/40 after, all four sizes |
| 2 | Held grant denied by a later save: preview, pane flag | Pass |
| 2 | Held grant: refusal at mint, reason in the conversation | Not exercised live: no dispatch here. Covered by `TestAdmissionRefusesWhatTheCapabilityPolicyDoesNotAdmit` |
| 3 | Denied default dropped on filing | Was silent. **Fixed** by this branch's first commit (`0cefbf96`): a note on the task's thread |
| 4 | Agent approves a `requires_person` task | Pass: 403 naming the grant. A person's approve queues it |
| 5 | Scope-set removal lists a task, template, schedule and workflow job; Cancel keeps it | Pass |
| 6 | Policy save on a daemon with grain/config; picker refresh after the PR merges | Pass. The editor is read-only while settings are locked |

## Steps and observations

### Gap 1: every picker surface

How each picker asks (`GrantsPicker`'s `runKind`/`policyRepo`; the plan's
`policySubject` is the pair of them):

| Surface | runKind | policyRepo |
|---|---|---|
| New task (`NewTaskOverlay.jsx`) | `task` (default) | the form's repo, or the default target; none for No repo |
| Template editor (`TemplateOverlay.jsx`) | `task` | the template's repo |
| Schedule editor (`ScheduleOverlay.jsx`) | `task` | the schedule's repo |
| Task pane (`detail/CapabilityToggles.jsx`) | `task` | the task's repo |
| Repo default grants (`RepoPage.jsx`) | `task` | the page's repo |
| Repo CI grants (`RepoPage.jsx`) | `ci` | **none before this branch**; the page's repo after |

No caller files a run of kind `process` (`model.CapabilityRunProcess` is used
only by the policy and the listing), so `task` is right for the template and
schedule editors: what they file are tasks.

`GET /api/capability-methods?kind=…&repo=…` over HTTP matched the rules for
every kind and repo combination, e.g. kind `task` on `acme/widgets`:

- `cloud-viewer`: "the capability policy denies …#cloud-viewer to this task on acme/widgets (rule 1: deny …#cloud-viewer)";
- `cloud-key`: "the capability policy does not admit …#cloud-key for this task on acme/widgets".

On `acme/gadgets` both were admitted for every kind (before rule 3).

In the browser, on `acme/widgets`, New task, Template, Schedule and Repo default grants
each listed `cloud-viewer` and `cloud-key` disabled with those reasons and a
Settings → Capabilities link, at all four sizes. The task pane (task on
`acme/gadgets`, after rule 3) listed `cloud-viewer` disabled with rule 3.

- **Bug A: the repo page's CI grants were not judged for the repo.**
  Repro:
  1. Save rule 2 (allow `cloud-key` on `acme/gadgets` only).
  2. Open `/repos/acme/widgets#ci` → CI grants → Add grant….

  *Before*: `cloud-key` was offered (not disabled) as "may be refused by the
  capability policy for CI jobs", though every CI job on `acme/widgets` would
  be refused it. The picker passed `runKind="ci"` but no `policyRepo`, so it
  only ever read the kind-wide listing.

  *After*: it passes the page's repo, and `cloud-key` is disabled with "the
  capability policy does not admit …#cloud-key for this CI job on
  acme/widgets".

- **Bug B: "may be refused" on a method admitted for the picker's repo.**
  Repro:
  1. Save rule 2.
  2. Open the pane of a task on `acme/gadgets` → Add grant….

  *Before*: `cloud-key` carried "may be refused by the capability policy for
  agent tasks". The listing had judged that exact run (kind `task`, repo
  `acme/gadgets`) and found nothing to refuse. The kind-wide verdict, true
  only of other repositories, was shown anyway. The same was true of every
  task/process picker with a repo, and of a held grant's flag.

  *After*: once a repo is known, a task or process picker drops the kind's
  verdict. A CI picker keeps it, because a CI job's trigger event is still
  unknown and event rules can refuse it.

- **Wording, fixed**: the policy's refusal for a CI run read "to this ci on
  acme/widgets" (`CapabilitySubject.describe`). It now reads "this CI job".

- **Observation, not changed**: the theme lowercases everything inside a
  `ButtonBase` (`ui/src/theme.js`), menu items included, so the refusal
  caption renders "for this ci job … (settings → capabilities)". It is house
  style, but it also lowercases the capability ID and rule pattern quoted in
  the reason, which a reader could copy wrongly where an ID has capitals.

Bounding boxes: for every disabled row, the row lies inside the menu's box
and `.method-policy-verdict` inside the row (±1 px), at 1440 and 390, light
and dark. That was 36 rows before the fixes and 40 after: the CI picker's
`cloud-key` became one.

### Gap 2: a held grant becoming denied

- Task on `acme/gadgets` filed holding `cloud#cloud-viewer` (admitted then).
- `POST /api/capability-policy/preview` with rule 3 added →
  `{"denied":[{"kind":"task","id":…,"grant":"…#cloud-viewer={}","reason":"…
  (rule 3: deny …#cloud-viewer)"}]}`.
- After `PUT`, the task pane flags the grant "denied by policy" with the
  reason and a link, at all four sizes. Its menu lists the method disabled.
- Saving the policy posts nothing to the holder's conversation. The reason
  reaches the conversation at admission: `capabilityAdmitter` runs
  `policy.Permits` per grant and refuses with a `RefusedError`, which comments
  on the task (`TestAdmissionRefusesWhatTheCapabilityPolicyDoesNotAdmit`). It
  was not exercised live, because neither server here dispatches.

### Gap 3: defaults dropped silently vs. loudly

- The deployment defaults were set to `self-debug, gcp-key, gemini-key,
  cloud#cloud-viewer`.
- `POST /api/tasks {"repo":"acme/widgets"}` (by default) → 201 with
  `capabilities: [gcp-key, gemini-key, self-debug]`.
- **Before** this branch's first commit, nothing said `cloud-viewer` was
  dropped.
- **After** it (`0cefbf96`, `TestCreateTaskReportsADefaultThePolicyDrops`),
  the task's thread carries: "Default grant …#cloud-viewer was not attached
  to this task: the capability policy denies …#cloud-viewer to this task on
  acme/widgets (rule 1: deny …#cloud-viewer)."
- The 201 body itself still has no note field. The thread is where a filer
  looks, and where the same note for a dropped GCP key already goes.
- An explicit denied grant is refused 400 with the rule:
  `POST /api/tasks {"capabilities":["…#cloud-viewer"]}` on `acme/widgets` →
  `the capability policy denies … (rule 1: deny …#cloud-viewer)`.

### Gap 4: `requires_person` approval

1. Task 21 was filed by a person, holding `api:tasks:{admin,write,read}` and
   `gemini-key`.
2. As task 21's agent, I filed `{"approved":true,"capabilities":["gemini-key"]}`.
   That gave 201 `proposed`, with the body's note "Filed for a person to
   approve: settings/capabilities.yml marks gemini-key as a person's decision
   (requires_person)."
3. Agent `POST /api/tasks/22/approve` → **403** "task 22 holds gemini-key,
   which settings/capabilities.yml marks as a person's decision
   (requires_person): only a person may approve it". The state stayed
   `proposed`.
4. Agent `run-now` → 403 at the scope gate (`dispatch:write` not held),
   before the catalogue is asked.
5. Person `POST /api/tasks/22/approve` → 200, `queued`.

### Gap 5: scope-set removal confirm

- `PUT /api/api-scope-sets` created `walk/held` (`tasks:read`, repos
  `acme/widgets`).
- I granted `api:walk/held` to a task on `acme/widgets`, a template, a
  schedule on `acme/widgets`, and a workflow job: `PUT
  /api/repos/acme/widgets/workflows` with `jobs.lint.capabilities:
  ["api:walk/held"]`.
- `GET /api/api-scope-sets/usage` listed all four, with kinds `task`,
  `template`, `schedule`, `workflow`.
- Settings → Capabilities → API scope sets → Remove, at 1440 and 390:
  - the dialog said "4 grants hold api:<set>, and will grant nothing once it
    is removed:" and listed each holder (`data-holder="kind:id"`);
  - it fits the viewport;
  - Cancel closed it, and after a reload the set is still there.

  This is now in `ui/e2e/capability-policy-pickers.spec.js`.

### Gap 6 (step 5): policy save as a config write, on a real daemon

The §2 walker's report (`docs/test-runs/2026-10-31-config-repo-write-path.md`)
covers settings, templates and `config-policy.yml`, but not the capability
policy, so this was run here.

- **The editor is read-only while settings are locked**, as §2 found for the
  other settings pages. Settings → Capabilities → Capability policy shows its
  rules with Add rule and Save policy disabled, at 1440 and 390. A person does
  not reach a refusal from it.
- **Refusal path over HTTP**:
  - `PUT /api/capability-policy` with a deny rule → **409**
    `config_managed_externally`, "this setting is not written directly: …
    Commit this change to tables/grain_config.json instead", with the
    `change` patch.
  - `POST /api/state-repo/pull-requests {"changes": <change>}` → 201 (task 2,
    `grain/task-2`, validation passed).
  - `POST /api/tasks/2/submit` → 200, and the task closed.
  - After the import, `GET /api/capability-policy` carries the rule, and
    `GET /api/capability-methods?kind=task&repo=acme/widgets` refuses
    `cloud-viewer` with it.
- **Picker cache across the merge**:
  1. A grain/config task's pane was opened, and its Add grant… opened once,
     which primes the listing cache: `cloud-viewer` was offered.
  2. A second config PR denying `cloud-viewer` on grain/config was opened and
     submitted over HTTP while the page stayed open.
  3. Once the import landed (a few seconds after submit), reopening Add
     grant… on the same page listed it disabled with "… to this task on
     grain/config (rule 3: …)": opening the menu reads past the cache.

  A grant the task already held (`cloud-key`, denied by the PR before) was
  flagged on the page's first load. A pane already open does not re-flag a
  held grant until the listing is read again (menu opened, or reload).
- The daemon's `tables/grain_config.json` patch quotes the `mcp_servers`
  column as diff context. It carried the demo server's bearer token in
  plain text, because I attached that server with a literal `Authorization`
  header rather than a `$secret:` reference. It was the operator's own
  choice, but a refusal's patch repeats it to whoever reads the refusal.

## Changes on this branch

- `fix(ui): tell the filer when the capability policy drops a default grant`
  (`0cefbf96`, from an earlier attempt): gap 3.
- `fix(ui): judge the repo's CI grants by the policy for that repo`: bugs A
  and B and the "this CI job" wording. It adds a `GrantsPicker.test.jsx` case
  and extends `TestCapabilityPolicyDenyRuleRefusesWhatItCovers`.
- `ui/e2e/capability-policy-pickers.spec.js`:
  - the held-grant flag, the repo-conditional verdict in the task pane, and
    the repo CI grants judged for the repo, each with bounding boxes at 1440
    and 390;
  - the four-holder scope-set removal confirm with Cancel.

  Before the fix the first test fails (on the `data-policy-verdict` of the
  admitted method); with it, both pass.

## Screenshots

These are from the walk script, before and after, for `/` (New task),
`/processes` (template and schedule editors), `/tasks/<id>`,
`/repos/acme/widgets#agents` and `/repos/acme/widgets#ci`, each with Add
grant… open, at 1440×900 and 390×844, light and dark. The crops attached to
the pull request are:

- the repo CI grants menu at 390 light, before and after;
- the task pane menu at 1440 light, before and after.
