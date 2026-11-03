# 2026-10-29: Capability catalogue and config-PR human Submit (test plan §3)

Task 1797 (`grain/049f1e09/task-1797`), walking §3 of
`docs/test-plans/2026-10-29.md`: the `settings/capabilities.yml` capability
catalogue (PR #107, design #88) and the config-repo auto-merge policy
(`settings/config-policy.yml`, PR #92). It ran against a real `grain daemon`
(for the config repo, which the demo does not have) and a real `grain demo`
(for repository-scoped checks against `-github-sim` and the browser).

## Commit and environment

- **Base tested**: `2f53e63a` (`main`, "Merge pull request #130 from
  ai-on-gke/grain/049f1e09/task-1732"). Fixes are on this branch.
- **Build**: `cd ui && npm ci && npm run build && cd .. && go build -o
  /home/debian/bin/grain ./cmd/grain`.
- **Servers**:
  - `grain daemon -data-dir /var/tmp/w/data -ui-addr 127.0.0.1:18090
    -sandbox-port-base=0 -as alice -agy-path /bin/true
    -gemini-api-key-file <dummy> -github-host 127.0.0.1:9
    -github-insecure-http -disable-native-sandboxes`. It hosts a real
    internal `grain/config` (`/var/tmp/w/data/state-repo`). It has no
    GitHub credential, so only no-repo tasks and `grain/config` tasks
    can be filed, and no agent backend, so no run gets past "building the
    agent framework".
  - `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:18080
    -open=false -access-code-file /var/tmp/d/code`. The plan's
    `-sandbox-port-base=0` is a daemon flag that `grain demo` refuses
    ("flag provided but not defined"). The demo needs no port base.
- **Operator access to the config repo**: `git -C
  data/state-repo config receive.denyCurrentBranch updateInstead`, then a
  clone at `/var/tmp/w/cfg` that pushed to `main` and to task branches, and
  `POST /api/state-repo/sync`.
- **Callers**: the access code (a person). For an agent, the access code
  plus `X-Grain-Acting-Task: <id>` (and `X-Grain-Acting-Reason`). That is
  the header the sandbox API proxy sets, so the request goes through
  `authorizeAgentCall` exactly as a run's call would. External agent tokens
  (`gma_…`, `POST /api/external-agents/tokens`, scopes `tasks:*`) were also
  used, after `PUT /api/settings {"externalAgentApi":true}`.
- **Browser**: Playwright (`ui/node_modules`) with Chromium from
  `/var/cache/playwright`, at 1440×900 and 390×844, light. The script was ad
  hoc (`/var/tmp/walk/walk.mjs`, outside the repository).
- **Stubs**: only `githubsim` (the demo's). Fake CI never runs a job.

## Catalogue used

Committed to the daemon's `grain/config` `main` as an operator would:

```yaml
defaults:
  task: [deployment-health, self-debug]
  ci: [deployment-health]
in_process:
  self-debug:   {restrictions: {repos: ["acme/widgets", "none"]}}
  grain-log:    {restrictions: {requires_person: true}}
  self-repair:  {restrictions: {kinds: [director]}}
  gcp-key:      {restrictions: {repos: ["acme/widgets"]}}
api_scopes:
  raw_scopes:
    "*:admin": {kinds: [task, director], requires_person: true}
```

That is (a) `gcp-key` refused outside one repo, (b) `grain-log`
`requires_person`, and (c) a CI default. The same policy JSON, as the
daemon imported it, was then loaded into the demo with `PUT
/api/capability-policy`.

## Outcome

| # | Check | Outcome |
|---|---|---|
| 1 | Round trip: commit → import → export | Pass |
| 2a | 400 on a refused explicit grant (HTTP, person) | Pass |
| 2b | Refused default dropped | Pass |
| 2c | `requires_person` grant from an agent's own filing → proposed with note | Pass |
| 2d | Agent approving that proposal | **Bug 1, fixed**: it was queued and dispatched |
| 2e | Agent attach of a `requires_person` grant → 403; person attach of a refused grant → 400 | Pass |
| 2f | Repo-move `PATCH` re-judges | Pass |
| 2g | Dispatch hold on a task queued before the tightening | Pass |
| 2h | CI job judged as kind `ci` | Not exercised live here. Pass in the task 1828 addendum |
| 2i | End-of-run proposal replay | Not exercised live here. Pass in the task 1828 addendum, plus **Bug 4, fixed** |
| 2j | External agent (`gma_`) filing | Pass: refused before the catalogue is asked |
| 3 | `staterepo.Check` refusals (`grain state check`) | Pass. Two wording nits |
| 4 | Config-policy hold on config PRs (live) | Partly: PRs opened. Hold not reached without an agent backend. Pass in the task 1828 addendum, UI at 1440 and 390 |
| 4b | `config-policy.yml` ceiling refused by Check | Pass |
| 5 | Settings → Capabilities rule editor save | **Bug 2, fixed**: erased the whole catalogue |
| 6 | New-task form, picker, refusal in the UI (1440, 390) | Refusal shown readably. **Bug 3** (follow-up): "No repo" sends hidden defaults |

## Steps and observations

### 1. Round trip

- Before the commit, `GET /api/capability-policy` was `{"policy":{}}`.
- I pushed the catalogue to `main` and called `POST /api/state-repo/sync`.
  - The policy then carried `catalogue` with every entry.
  - `/api/config` `defaultCapabilities` became `[deployment-health,
    self-debug]`, and `tables/grain_config.json` `default_capabilities`
    became `deployment-health,self-debug`.
- The daemon's next export ("Update grain state") rewrote the file in
  canonical form:
  - It added the header comment.
  - It listed the six built-in `servers:` entries.
  - It wrote empty `repos: {}` and `presets: {}`.
  - Restrictions and defaults came back unchanged.

  The import log names the changed `grain_config` row (`default_capabilities
  was '' and is now …`).
- A direct push to `main` is imported without `staterepo.Check`. Pushing
  `self-debug` with `repos: [acme/widgets]` alone was taken up. The five
  seeded `system:*` schedules all carry `self-debug` on no repo, which
  Check would have refused on a PR (see 3). Check gates pull requests only.
  An operator pushing straight to `main` is trusted, as designed.

### 2. Enforcement

All refusals name the rule and the file. Examples, verbatim:

- Daemon, person, `{"noRepo":true,"capabilities":["gcp-key"]}` → `400`:
  `capability gcp-key is not granted on none by settings/capabilities.yml
  (repos: acme/widgets)`. On this branch it reads "…not granted on a task
  with no repository (none)…".
- `self-repair` on a task → `400` `capability self-repair is not granted to
  a task by settings/capabilities.yml (kinds: director)`.
- **Dropped default**: on the demo, whose deployment defaults are
  `self-debug, gcp-key, gemini-key`, a no-repo task filed with no list got
  `[gemini-key, self-debug]`. `gcp-key` was dropped silently. On
  `acme/widgets` it kept all three.
- **Person and `requires_person`**: a person filing `grain-log` approved got
  `queued` (task 4). A person's filing is the person's decision, as
  designed.
- **Agent filing**: an acting task 8 (person-approved, holding `grain-log`
  and `api:tasks:{read,write,admin}`) filed `{"approved":true,
  "capabilities":["grain-log"]}` → `201` `proposed`. The body ended:

  > _Filed for a person to approve: settings/capabilities.yml marks
  > grain-log as a person's decision (requires_person)._
- **Bug 1**: the same agent then called `POST /api/tasks/9/approve` and got
  `200`, state `queued`. The daemon log shows `run 9-2 (task 9): … building
  the agent framework`: dispatch picked it up. Nothing at dispatch asks
  whether a person approved a `requires_person` grant (`catalogueHold` only
  re-judges refusals). The same hole was open through Run now
  (`agentMayApprove`).
- **Attach**: an acting task 10 holding `api:capabilities:admin` attached
  `grain-log` to task 3 → `403` `capability grain-log requires human
  approval (settings/capabilities.yml marks it requires_person)`. A person
  attaching `self-repair` → `400` (kinds: director). A person attaching
  `grain-log` → `200`.
- **Repo move** (demo, after `PUT /api/deployment
  {"targetRepos":["acme/widgets","acme/gadgets"]}`): moving a task holding
  `gcp-key` to `acme/gadgets` → `400` `this task cannot be moved onto
  acme/gadgets: capability gcp-key is not granted on acme/gadgets by
  settings/capabilities.yml (repos: acme/widgets)`. A task holding
  `gcp-key` only as a *default* is refused the same way rather than having
  it dropped. Filing drops a refused default, but moving does not. Worth
  knowing, and not a hole.
- **Dispatch hold**: task 11 was filed and queued while `self-debug` allowed
  `none`. I then pushed a catalogue without `none`. On the next tick:
  - The daemon log said `dispatch: candidate 11 skipped: held by
    settings/capabilities.yml: capability self-debug is not granted on none
    by settings/capabilities.yml (repos: acme/widgets)`.
  - The task's activity line read the same, ending `(capability-catalogue
    grant)`.
  - The task stayed `queued`, not dispatched.
- **External agents** (`gma_`, `tasks:write` or `tasks:admin`) cannot hand
  out what their token does not hold:
  - `gcp-key` and `grain-log` → `an agent cannot grant gcp-key, which its
    own task does not hold`.
  - `api:tasks:admin` → `api:tasks:admin is a dangerous api grant: only a
    person may grant it`.

  So the catalogue is never reached from that path.
- **Not exercised live**:
  - CI jobs as kind `ci` (`CheckCIJobCatalogue`): fake CI runs nothing, and
    the daemon reaches no repository.
  - End-of-run proposal replay (`finish.go`): no run finishes without an
    agent backend.

  Both rest on their unit tests.

### 3. `staterepo.Check` refusals (`grain state check .` on a clone)

| Bad file | Refusal |
|---|---|
| Unknown top-level key | `parsing settings/capabilities.yml: yaml: unmarshal errors: line 82: field bogus_section not found in type staterepo.capabilitiesDoc` (names a Go type; still actionable by line) |
| Secret by value in `servers[].secrets` | `servers[2] (https://grain.dev/capabilities/gcp): secrets.service_account must be a $secret:<name> reference, never a value` |
| Unknown kind | `in_process.self-repair: kind "wizard" is not one of task, session, director, ci, process, review` |
| `ci_only` with other kinds | `in_process.grain-log: ci_only is set, so kinds must be [ci], not [task, ci]` |
| A default its own rules refuse | `defaults.task: capability self-repair is not granted to a task by settings/capabilities.yml (kinds: director)` |
| Server listed twice | `servers[3]: https://grain.dev/capabilities/gcp is listed twice; servers[3]: server https://grain.dev/capabilities/gcp is listed twice` (said twice) |
| Template carrying a refused grant | `template repair grants self-repair: capability self-repair is not granted to a task by …` |
| Tightening that refuses a seeded schedule | `schedule system:infra-review grants self-debug: capability self-debug is not granted on none …` (one clause per schedule) |
| `config-policy.yml` listing `settings/*.yml` | `auto_merge_paths[1] "settings/*.yml" matches NeverAutoMergePaths entry "settings/config-policy.yml" (never allowed to auto-merge)` |

A method whose `requires_person_when` names a value its `args.allowed` does
not admit passed Check. That is harmless, because the value can never be
granted, but it is unreachable configuration.

### 4. Config-policy hold

- I committed `settings/config-policy.yml` to `main` with `auto_merge_paths:
  [memory/**, scripts/**]`. This was a direct push, so Check did not refuse
  the ceiling-breaking `scripts/**`.
- I filed tasks 12–14 on `grain/config` and pushed their branches, which
  touch:
  - `memory/walk-note.md` (opted in);
  - `notes/unlisted.md` (unlisted);
  - `scripts/walk.sh` (in `NeverAutoMergePaths` although listed).
- `POST /api/tasks/{id}/pull-request` opened native `grain/config#12`–`#14`.
- The security hold is judged when a task's run finishes and it goes to
  merge. With no agent backend these tasks never got there, so the
  human-Submit hold could not be watched live or in the UI.
- From the code: `stateRepoGuard` → `ClassifyState` checks the ceiling
  first and reads the policy through `StateRepoPullRequestClient.ReadFile`,
  which forces `settings/config-policy.yml` to the default branch whatever
  ref it is asked for.

  This is covered by `pkg/daemon/statepullrequests_diff_test.go` and the
  trust-boundary tests. A follow-up is filed to walk it live.

### 5. Bug 2: the rule editor's Save erased the catalogue

- Settings → Capabilities' rule editor (`CapabilityPolicyEditor.jsx`) saves
  `{"policy":{"rules":[...]}}`, and `SetCapabilityPolicy` replaced the whole
  policy.
- On the daemon, `PUT /api/capability-policy {"policy":{"rules":[]}}`:
  - answered `{"policy":{}}`;
  - the next export deleted `settings/capabilities.yml` from `grain/config`;
  - a no-repo task holding `self-repair` (director-only a moment before)
    was then filed without complaint.

  Every restriction, `requires_person` included, was gone after a save
  about something else.

### 6. Browser (demo, catalogue loaded)

- **New-task form** at 1440 and 390:
  - Title "UI: gcp key on no repo", Repos → "No repo", Create task.
  - The form posted `"noRepo":true,"capabilities":["self-debug","gcp-key","gemini-key"]`
    and got `400`.
  - The page showed `capability gcp-key is not granted on none by
    settings/capabilities.yml (repos: acme/widgets)` beside the button.
    Bounding box: 414×56 at x=530 (1440), and 285×76 at x=69 (390).
  - `scrollWidth` was 1440 and 390, so nothing overflows.
- **Bug 3** (follow-up, not fixed here): with "No repo" chosen the form
  hides the legacy-capability checkboxes but still sends the pre-ticked
  defaults as an explicit list.
  - The server then refuses them as asked-for (400) rather than dropping
    them as defaults.
  - A person cannot untick what is hidden, so a no-repo task cannot be
    filed from the form at all while the catalogue keeps a deployment
    default to a repository. Over HTTP with no list it files and drops the
    default (2b).
- **Grants picker** ("add grant…") lists the capability server methods
  (`cloud-key`, `cloud-viewer`, `gcp-key`) with nothing marking what the
  catalogue refuses for the chosen repo. The refusal arrives only on
  submit.
- I also shot the task page (`/tasks/21`) and Settings → Capabilities
  (`/settings/capabilities`) at both widths. No change to `ui/` was made
  here.

## Findings, fixes and follow-ups

1. **Fixed: an agent could approve a task holding a `requires_person`
   grant.**
   - `Client.Approve` and Run now's `agentMayApprove` now refuse an agent
     (403) when the catalogue marks any grant the task holds as a person's
     decision. The refusal names the grants.
   - A person approving is unchanged.
   - Pinned by `TestCatalogueRequiresPersonRefusesAnAgentsApproval`, which
     fails on the base.
   - This is the agent API's approval path, so it wants a security review.
2. **Fixed: saving the capability policy's rules erased the catalogue.**
   - `SetCapabilityPolicy` keeps the stored catalogue when the request
     carries none.
   - Pinned by `TestSetCapabilityPolicyKeepsTheCatalogue`, which fails on
     the base.
   - Related, and not changed: an agent holding `api:capabilities:admin`
     can still replace the catalogue by sending one. The design
     (`design/design/config-repo-settings.design.md` §2.2) has this route refused
     with 409 in favour of a config PR.
3. **Fixed (wording)**: the no-repo refusal said "on none". It now says "on
   a task with no repository (none)".
4. **Follow-up**: the new-task form's "No repo" sends hidden pre-ticked
   defaults (Bug 3), and the picker does not show what the catalogue
   refuses.
5. **Follow-up**: walk the config-policy human-Submit hold live (gap 4),
   the CI `ci` kind and end-of-run replay. All three need a daemon with a
   scripted agent backend and a reachable repository.
6. **Nits, not changed**:
   - The duplicate-server Check message is repeated.
   - The unknown-key message names `staterepo.capabilitiesDoc`.
   - Repo move refuses rather than drops a refused default.
   - The plan's `grain demo … -sandbox-port-base=0` is not a demo flag.

## Addendum: task 1828 (the three gaps, with runs that finish)

Task 1828 (`grain/049f1e09/task-1828`) walked the three checks above that
need runs to finish: 2h, 2i and 4.

### How

- **Base**: `94f69ba9` (`main`).
- `grain daemon` still has no scripted-agent backend, and `grain demo` never
  runs an agent. So the walk runs on the e2e harness instead:
  `tests/e2e/config_policy_catalogue_walkthrough_test.go`. It uses:
  - the real orchestrator (`RunCycle`: dispatch, finish, security hold,
    merge queue);
  - a real git server with githubsim behind it;
  - a scripted antigravity agent that clones, edits and pushes;
  - and, for CI, the daemon's own `daemon.BuildCIService`, with a local
    sandbox standing in for a machine.
- The catalogue is not stuffed into the store. Each test exports the
  store's settings (`staterepo.Export`), writes `settings/capabilities.yml`
  and imports it back with `staterepo.ImportTables(SettingsTables)`, which
  is how the daemon applies a merged config change.
- The config repository is the world's repository, named as this
  deployment's state repository through `model.Store.SettingsRepo`. That is
  the hook the daemon sets from its state manager, and `stateRepoGuard`
  reads it as it reads `grain/config`.
  - `settings/config-policy.yml` was committed straight to `main` with
    `auto_merge_paths: [memory/**, scripts/**]`.
- Run with: `go test ./tests/e2e/ -run
  'TestConfigPolicyHoldWalkthrough|TestCICatalogueWalkthrough|TestCatalogueProposalReplayWalkthrough' -v`.
- **Mutation checks**: each test was run against a broken build, and each
  failed:
  - CI catalogue check off, or `defaults.ci` not joined: all three CI
    cases fail.
  - `notes/**` forced into the policy: the unlisted and self-opt-in cases
    fail.
  - Policy never read: the `memory/**` case fails.
  - The Bug 4 assertion fails on the base.
- **UI**: a temporary (uncommitted) test served `ui.NewServer` over the same
  kind of world after its runs had finished. Pages were shot with
  `ui/e2e/screenshot.mjs --url … --code …` and measured with Playwright.

### Results

| # | Check | Outcome |
|---|---|---|
| 4a | Config PR touching `memory/x.md` (opted in) | Pass: kept auto-merge, no hold, no review filed, merged and closed |
| 4b | Config PR touching `notes/unlisted.md` (unlisted) | Pass: `awaiting_submit`, auto-merge off, not merged over 6 clean cycles |
| 4c | Config PR touching `scripts/x.sh` (listed, but past the ceiling) | Pass: held the same way |
| 4d | Branch that adds `notes/**` to `auto_merge_paths` and touches `notes/x.md` | Pass: held. The hold names both `settings/config-policy.yml` and `notes/x.md`, so the branch's own policy was not read |
| 4e | Hold in the UI, task page and Inbox, 1440 and 390 | Pass (below) |
| 2h-1 | CI job holding a grant with `kinds: [task]` | Pass: run `errored`, no sandbox built |
| 2h-2 | CI job holding a grant with `repos: [acme/other]` | Pass: the same |
| 2h-3 | `defaults.ci` joins the repo's CI capabilities | Pass: with no repo list, the job's sandbox request held `[ci-default-key]` and the run succeeded |
| 2i-1 | Proposal for a `requires_person` grant (`grain-log`) from an auto-merging parent that holds it | Pass: `proposed`, holds the grant, "_Filed for a person to approve: settings/capabilities.yml marks grain-log as a person's decision (requires_person)._" |
| 2i-2 | Proposal for a grant the catalogue refuses it (`gcp-key`, `templates: [walk-ops]`; the parent is on that template, the proposal on none) | Pass: `proposed`, keeps the grant, "_Filed for a person to approve: settings/capabilities.yml refuses what it asked for (capability gcp-key is not granted to an ad-hoc task by settings/capabilities.yml (templates: walk-ops))._" |
| 2i-3 | Plain proposal in the same run | Pass: `queued` |
| 2i-4 | Those held proposals marked as the run's own `hold_for_approval` | **Bug 4, fixed** |

**What the hold says.** Each held change got one comment:

> acme/widgets#9000 touches grain's trust boundary, so it is no longer
> merging automatically. … **state-privilege** (built in) -- this
> deployment's own settings … applied to the running daemon once merged:
> `notes/unlisted.md` … Filed task 3 to review it, but changes to
> **state-privilege** never merge automatically: press Submit once you
> have read the review.

A security review is still filed. When it finished clean, it did not
release the hold, and no releasable `security-hold` link was set.

**CI refusals, verbatim:**

- The job error and the daemon log line read: `waiting for this job's
  capability grants: the capability catalogue refuses this CI job:
  capability task-only-key is not granted to a CI job by
  settings/capabilities.yml (kinds: task)`.
- The repo-restricted case reads `… other-repo-key is not granted on
  acme/widgets by settings/capabilities.yml (repos: acme/other)`.
- The daemon log also has `grain ci: acme/widgets: push job … refused by
  settings/capabilities.yml: …`.

**UI** (the held world: task 1 on `memory/`, task 2 on `notes/`, task 3 on
`scripts/`; light and dark; `/tasks/1`, `/tasks/2`, `/tasks/3`, `/inbox` at
desktop 1440×900 and phone 390×844):

- **Task page** (`/tasks/2`, `/tasks/3`):
  - The timeline shows the hold comment above.
  - The action panel reads "This change touches grain's trust boundary and
    will not merge automatically: read its security review, then Submit.
    Security review: task 5", with **submit** below it.
  - At 1440, the panel is at x=1152, 260 px wide (y 124–680). The text sits
    at y=184, and the submit button is 260×37 at y=315, inside the panel.
  - At 390, the panel is 357 px wide at x=17. The text sits at y=1322, above
    the 357×37 submit button at y=1417 (below the fold, so it scrolls).
  - `scrollWidth` is 1440 and 390: nothing overflows.
- **Inbox**:
  - The group is "asked: submit for merge (2)", with "submit all (2)".
  - It has one row each for tasks 2 and 3 ("Asked you to submit
    acme/widgets#9001" / "#9002"). Each row has a 64×31 **submit** button
    in the right-hand slot: x=1348 of 1440, and x=310 of 390.
  - Task 1 is not there. Its page shows it merged and closed.
- I could not look at the PNGs myself (no image reader in this sandbox, and
  no Gemini key for `LOOK=1`). The checks above are DOM text and bounding
  boxes. Crops and the contact sheet are attached to the task's closing
  note.

### Bug 4: grain's own end-of-run holds were marked as the run's choice

**What happened.** In `relayProposedTasks` (`orchestrator/finish.go`), every
proposal grain filed for a person for a reason of its own was also handled
as if the run had set `hold_for_approval`. That covered the catalogue's
`requires_person` and refusals, an unresolvable fork head, and an unusable
change target. Each such proposal got two things:

- the `held-for-approval` tag;
- this note:

  > grain: task 1, which proposed this, could have queued it on its own
  > permission and asked for it to wait for a person instead
  > (propose_task's hold_for_approval).

The run had asked for no such thing. The tag's own contract
(`model.HeldForApprovalTag`) is that it marks a choice the run made. The
live path (`ui.CreateTask`) already keeps the two apart.

**Fix.** Only the run's own `hold_for_approval` now earns the tag and the
note. grain's reasons still file the proposal for a person, under the
"Filed for a person to approve" note that says why. Pinned by
`TestCatalogueProposalReplayWalkthrough`, which fails on the base.

### Nits seen, not changed

- For a state-privilege hold, the daemon log says `auto-merge withdrawn
  pending security review` (`securityhold.go`). No review releases that
  hold, so the line suggests a wait that never ends on its own.
- A CI job the catalogue refuses has its error prefixed `waiting for this
  job's capability grants:` (`grit/engine`). The job is not waiting: it
  errored.

### Still not walked

- On a real `grain daemon` binary: it has no scripted-agent backend
  (`-agy-path` takes only a real CLI). These walks use the same
  orchestrator, CI service and UI server the daemon wires, in-process.
- A config PR on the literal `grain/config` name through the daemon's
  internal state repository, as task 1797 opened them. The guard treats
  that name and `SettingsRepo` the same way (`isSettingsRepo`).
