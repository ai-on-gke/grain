# grain's key user journeys

This is a catalogue of the journeys people take through grain most often, or
where grain most often goes wrong for them. It is written for a **nightly
simplification review**: a check, run each night, that walks each journey,
counts its steps and friction, and proposes ways to make it shorter, with
fewer screens, fewer decisions and fewer ways to fail. That suite isn't built
yet. Its proposed design is at the [end of this document](#proposed-nightly-simplification-suite).

Everything here was read from `main` at `7af974c29` (2026-10-15). Each
journey says where its facts come from. Where the code didn't settle a
question, the entry says **Unclear** instead of guessing. The step counts
are what the nightly suite should check first.

Related documents, linked rather than copied: the [README](../README.md)
("Get started", "First sign-in"),
[`grain init`](grain-init.md), [Connections and models](connections-and-models.md),
[the single-tenant reset](single-tenant-reset.md),
[release candidate management](release-candidate-management.md),
[schedules](schedules.md), [declarations](declarations.md) and the
[release UI walkthrough](ui-walkthrough.md).

## How each entry is laid out

- **Who and why**: the persona and their goal.
- **Starting state**: what exists before the first step.
- **Steps today**: numbered. A *decision* is a step where the person has to
  choose between options, and is marked **(decision)**. A step that needs a
  shell on the host is marked **(shell)**.
- **Done when**: the observable success condition.
- **Known friction**: operator reports (with their task numbers, and whether
  the fix is on `main`), plus problems found while reading the code.
- **How a walkthrough would check it**: what a Playwright walkthrough against
  `grain demo` can drive, and what needs a real host, real credentials or real
  GitHub, with a stand-in for each.
- **Simplification ideas**: candidates only. Nothing here is decided.

"Walkthrough" means the protocol in [ui-walkthrough.md](ui-walkthrough.md):
the real `grain demo` server (`go run ./cmd/grain demo -addr 127.0.0.1:0
-open=false`) on its embedded SQLite store, driven by Playwright against the
built frontend, with only the external boundaries stubbed. `grain demo` already
has the stand-ins most journeys need (`cmd/grain/demo.go`): `-unconfigured`
(no model, no git credential, as on a fresh install), `-github-sim`,
`-git-proxy`, `-fake-ci`, `-base-branch-ci` (turn a branch red or green),
`-merge-queue-states`, `-scheduled-workflows`, `-upgrade-image` (a real
upgrader against a local registry) and `-model-catalog` (a model-listing
stub).

---

## 1. Install grain on your own machine (host mode) and sign in

**Who and why.** An operator installs grain on a Linux machine they control
and signs in to the UI for the first time.

**Starting state.** A fresh Debian host with systemd and Docker, or one that
can install Docker. Ideally it has `/dev/kvm`. The operator has a GitHub
token and one model provider's credential.

**Steps today** (`scripts/setup.sh`, README "Get started"):

1. SSH to the host and `git clone` grain. **(shell)**
2. Pick the install mode: container (the default) or `GRAIN_INSTALL_MODE=host`. **(decision)**
3. Pick the channel: `latest` (the default since #3689), `candidate`, `main`,
   or a pinned `GRAIN_IMAGE_TAG`. **(decision)**
4. Pick the git credential: a GitHub token now, or a GitHub App, which is
   documented only under `setup.sh --help`. **(decision)**
5. Pick one agent provider and export its key (`GRAIN_OPENAI_API_KEY`, ...). **(decision)**
6. Export `GRAIN_TARGET_REPO` and `GRAIN_TARGET_REPOS`. The README example sets
   both to the same value without saying why both exist.
7. Pick the UI address: `0.0.0.0:4242` by default (#3701), or
   `127.0.0.1:4242` plus a tunnel (`scripts/client.sh`). **(decision)**
8. `sudo ... ./scripts/setup.sh`, then wait. **(shell)**
9. Read the readiness report: the URL, host names, a firewall hint, the
   admin account, and the password file.
10. `sudo cat /var/lib/grain/data/admin-password`. **(shell)**
11. Open `http://<host>:4242/`. Off-host, a firewall may need opening, and the
    installer never opens one.
12. Sign in on the **Log in** tab with **Username** = the `$SUDO_USER` account
    and that password (`ui/src/components/UsernameDialog.jsx`).
13. Change the password (user settings), then delete the password file. **(shell)**
14. To use the CLI without `sudo`, log out and back in so the new group
    membership takes effect. **(shell)**

`grain init` (building from source, [grain-init.md](grain-init.md)) replaces
steps 2–8 with one command and 14 numbered internal steps. It adds its own
prerequisites (`make`, Node/npm, `/dev/kvm` or `-platform=linux-container`)
and a second password file for `-admin-user`.

**Count: 14 steps, 5 decisions, 5 shell steps** before the first screen is
usable as an admin.

**Done when.** The operator is signed in with the access code,
`grain list` works in their own shell, and the readiness report has no
unresolved items.

**Known friction.**
- *No account had a password* (#3688, **fixed on main**). `setup.sh` now
  makes `GRAIN_ADMIN_USER` an admin with a generated password, and the sign-in
  dialog says when no account has one.
- *CLI hung* (wrong `GRAIN_SERVER` port, unreadable local token; #3688,
  **fixed on main**). `/etc/grain/cli.env`, a 5s connect timeout, and a
  `0640` token with group access are in place. Under a shared `GRAIN_GROUP`
  the token stays `0600` and the CLI still needs `sudo -u grain`, which is a
  second, different instruction.
- *README ports didn't match the installer* (#3701, **fixed**: everything is 4242).
- *Who is an admin, and the hidden Admin mode switch* (**fixed**: the
  switch is gone, single-tenant reset slice 3c, and the admin list with
  task 4880). Whoever holds the access code is the owner on every request.
- *Email vs admin username*. The dialog asks for a **Username**, and the
  account `setup.sh` makes is the Unix login (`$SUDO_USER`), not an email.
  **Unclear** from the code read whether an email-shaped name maps to that
  account anywhere (for example behind an identity-aware proxy).
- *Channel choice* (#3689, **fixed**). New installs follow `latest`, a re-run
  keeps the channel, and the Upgrade button defaults to it. It is still an
  up-front decision the operator has to understand.
- Found in the code: two password files on a `grain init` install
  (`admin-password` and `admin-password.<name>`), and "delete the file" is
  a manual step with nothing reminding the operator to do it.

**How a walkthrough would check it.** Steps 11–13 can be driven against
`grain demo -unconfigured`. That covers the no-password note, sign-in and
the password change. Steps 1–10 and 14 need a real host. A disposable Debian VM, or a container
with systemd and Docker, could run `setup.sh` in host mode end to end. The
script's own tests (`tests/installer`) cover pieces of it. A script can
count the environment variables the README example needs and the commands
between `git clone` and the first `200` from `/api/config`.

**Simplification ideas.**
- Generate the password and print a one-time sign-in link instead of a
  file to `sudo cat` and delete.
- Let the first sign-in from loopback prompt for a password.
- Fold `GRAIN_TARGET_REPO` and `GRAIN_TARGET_REPOS` into one variable, or
  make both optional, since repos can be added in the UI.
- Default to host mode, or drop the mode choice from the quick start.
- Move the model and git credentials out of the installer and into the
  setup alert's Settings tabs ([journey 4](#4-set-up-a-new-team-retired)), so the install
  needs no secrets at all.

---

## 2. Install with a site's own installer

A site can wrap `scripts/setup.sh` in an installer of its own, with that
site's answers. Google's Workstation installer is the one there is, and its
journey is in
the wrapper distribution's own docs.

---

## 3. Upgrade a deployment and confirm what is running

**Who and why.** An operator moves a deployment to a newer build and checks
which version is now live.

**Starting state.** A running deployment, with the operator signed in with
the access code.

**Steps today** (`UpgradePanel.jsx`, `AdminOverlay.jsx`):

1. Open **Settings → Upgrade** (`/settings/upgrade`, in the Deployment
   group), or click the build stamp at the foot of the sidebar.
2. Choose a branch or tag. The picker defaults to the channel the deployment
   follows (#3689). **(decision)**
3. Optionally tick "after tasks finish". **(decision)**
4. **Check the upgrade** to preview the target and its release notes
   (`UpgradeReleaseNotes.jsx`, #3064).
5. Click **Upgrade**, then confirm it in a dialog.
6. Watch the progress steps, then the restart.
7. Confirm the version: the build stamp at the bottom of the sidebar
   (`config.version`) or Settings → Upgrade's running build.

**Count: 7 steps, 2 decisions.**

**Done when.** The sidebar build stamp shows the target release or commit,
and the daemon is healthy.

**Known friction.**
- *rc labels after promotion* (#3064, **fixed on main**): promoted images are
  now stamped with the release version.
- *Host-mode unit settings are not refreshed by upgrades* (**still true**).
  `setup.sh` writes `grain-daemon.service` once, and an upgrade only repoints
  the image ref (see the comment above `verify_daemon_flags` in
  `scripts/setup.sh`). Environment that newer code expects on the unit, such
  as `GRAIN_INSTALL_MODE` for the ambient git credential
  (`pkg/gitproxy/ambient.go`), only arrives on a re-run of `setup.sh`. Nothing
  in the UI says so. The ambient credential is simply refused.
- *Upgrade was the last of System's tabs* (**fixed**): it is Settings →
  Upgrade now, fourth in the section nav and found by the settings filter.

**How a walkthrough would check it.** `grain demo -upgrade-image
localhost:5000/x/grain` drives steps 1–6 for real. It pulls, verifies and
health-checks, but never restarts. Step 7 after a real restart, and the
host-mode unit drift, need a real host install: `tests/container` and the
nightly `upgrade` job are the closest existing checks. A script can diff
the installed unit against what the current `setup.sh` would write.

**Simplification ideas.**
- Have the upgrade re-render the host-mode unit (or report the drift as an
  upgrade step, the way "unit flags" already is).
- Surface "a newer release is available" on the build stamp itself, with
  Upgrade one click away.
- Collapse "Check the upgrade" into the confirm dialog.

---

## 4. Set up a new team (retired)

This journey created a team and walked its setup checklist: the New team
dialog, Team settings' Git identity and Models & connections tabs, and
members. Teams went with the single-tenant reset
([single-tenant-reset.md](single-tenant-reset.md), stage 3): a grain
serves one deployment, and what this journey set up per team is the
deployment's own, on Settings → Models and Settings → Git
(`components/settings/ModelsSection.jsx` and `GitSection.jsx`). Its friction notes about adding a model
connection (slow CLI discovery, no lister in `grain demo`) still hold
there. A walkthrough of that setup belongs to `ui-walkthrough-settings.yml`.

---

## 5. Give tasks cloud access (GCP)

**Who and why.** The owner needs a task to reach Google Cloud, through the
`gcp-key` capability.

**Starting state.** A deployment with a repo whose tasks need GCP.

**Steps today** ([GCP keys](team-gcp-keys.md), `CapabilitiesPanel.jsx`):

Path A: the deployment's own service accounts.
1. Configure `gcp-key` on Settings → Credentials. **(decision)**
2. Grant `gcp-key` on the task or the repo's default capabilities.

Path B: a named key, for another project or a minter of its own.
1. Create the minter secret.
2. `POST /api/gcp-keys` or `grain gcp-key`, or `gcp_keys.yaml` in the
   state repository. **There is no UI for named GCP keys.** **(shell)**
3. Grant `gcp-key:<name>`.

**Count: 2 steps for path A, 3 for path B, which needs the API.**

**Done when.** The task starts with a working `~/.gcp-service-account.json`.

**Known friction.** Named keys have no UI, and the two paths are not
presented as a choice anywhere. (The old friction, `gcp-key` silently
limited to the `default` team, went with team capability scoping in
stage 4a.)

**How a walkthrough would check it.** Path A is drivable on `grain demo`.
Path B's API can be scripted against demo. A real mint needs a GCP
project; a stub minter secret is enough to check that the grant is
accepted.

**Simplification ideas.**
- A "Cloud access" section on Settings that offers both paths, with named
  keys editable there.

---

## 6. File a task and get it merged

**Who and why.** The owner asks for a change and wants it merged.

**Starting state.** A set-up deployment (connections on Settings) with a
repo on GitHub.

**Steps today** (`NewTaskOverlay.jsx`, `detail/Actions.jsx`, `InboxPage.jsx`):

1. **New task**, then Title and Description (attachments optional).
2. Repository and Base branch, or "Create a new branch". **(decision)**
3. "Auto-merge once checks pass". **(decision)**
4. Optional fields: "Don't port to the repo's paired branches",
   Capabilities, Target team (multi-team admins only), Model, and **Advanced
   options** (interactive session, connection, network, nothing-to-do
   action, review, prompt override, vCPUs/memory/disk).
5. "Queue immediately". Unchecked, the task is filed as a proposal that
   needs **Approve**. **(decision)**
6. **Add to backlog**.
7. Wait for dispatch, or click **Run now** ("pick up now": ahead of the
   queue, even if blocked, deferred or unapproved).
8. Answer questions if asked ([journey 7](#7-answer-an-agents-question-or-chat-with-a-running-task)).
9. The PR opens and grain CI runs. Without auto-merge, click **Submit**.
10. The merge queue batches, rechecks and merges. If it stops,
    **Retry merge** or **Force merge**.
11. The task closes.

If the base branch is red, the task gets a depends-on link to the branch's
repair task (#3086, **on main**) and shows "blocked on #N". **Run now**
overrides that.

**Count: 6 steps to file (3 decisions, about 20 optional fields), then 5 to
merge, of which 1–2 are clicks.**

**Done when.** The task is closed with its PR merged.

**Known friction.**
- The form is long. About 20 controls, many behind **Advanced options**, for
  a task that usually needs a title, a description and a repo.
- "Queue immediately" against Approve is a decision on every filing.
- Submit only appears when auto-merge is off. Whether a task will need a
  human Submit isn't visible until the PR is open (security holds, `.grain`
  changes, `awaiting_submit`).

**How a walkthrough would check it.** Drivable end to end on `grain demo
-github-sim -git-proxy -fake-ci -base-branch-ci -merge-queue-states`. Run the
agent through `POST /api/e2e/git-proxy/runs` and flip the base branch with
`/api/e2e/base-branch/red`. A real agent run and real GitHub CI are the
live suites' job (`live-agent`).

**Simplification ideas.**
- A minimal form (title, description, repo), with everything else editable
  on the task afterwards.
- Predict "this will need a human Submit" at filing time.
- Make auto-merge the per-repo default rather than a per-task tick.

---

## 7. Answer an agent's question or chat with a running task

**Who and why.** A person unblocks a parked task, or steers a running one.

**Starting state.** A task that asked a question (parked), or a running or
interactive one.

**Steps today** (`InboxPage.jsx`, `TaskChat.jsx`):

1. Notice the question: the **Inbox** count, or the row's state.
2. Open the **Inbox** and expand the question's reply box in place on the row.
3. Type the answer and send it. The reply puts the task back in the queue.

Or, for a running task:

1. Open the task.
2. Type in the chat composer ("Message…"). The comment reaches the agent
   mid-run, and its replies appear as comments.

**Count: 3 steps (Inbox), 2 (chat).**

**Done when.** The agent's next turn uses the answer (a new comment or
activity line), or the parked task is dispatched again.

**Known friction.** After answering, there is no feedback until the next
dispatch. It isn't clear how long the reply will take to land, and a
queued-but-not-dispatched answer looks like an ignored one. **Unclear**:
whether the inbox notifies outside the UI (e-mail, chat). No notifier was
found in this read.

**How a walkthrough would check it.** Drivable on `grain demo`: seed a parked
task and answer it from the Inbox. A run in flight needs the `-git-proxy`
e2e run, or a stubbed live session (as `ui-walkthrough-shell.yml` does).

**Simplification ideas.**
- After a reply, show "queued, position N", or start it at once ("Run now")
  as the reply's default.
- Notify the person a question is addressed to.

---

## 8. Find out why something is stuck

**Who and why.** Someone sees a task or a merge not moving and wants the
reason and the fix.

**Starting state.** A task queued for a long time, a PR not merging, or a
schedule that didn't fire.

**Steps today.** These depend on the cause. Each is a separate place:

1. *Task waiting to dispatch*: open the task and read its activity line.
   Grant waits read "waiting: ..." or "waiting for X (cap grant)"
   (`grantWaitActivity` in `pkg/ui/tasks.go`).
2. *Blocked on another task, or on a red base branch*: the "blocked on #N"
   link (#3086, #3451, **on main**), then open #N.
3. *Merge not moving*: the repo's **Merge queue** tab, which has one lane per
   base branch (#3340, **on main**), and the task's merge-queue state and note.
4. *Schedule didn't fire*: the **Schedules** page now records skipped firings
   (#3560, **on main**), and a held firing is noted on its blocker (#3748).
5. *Deployment-wide*: pause banners (agent, pipeline) and the System
   **Pipeline** tab.

**Count: 1–3 clicks once you know where to look, across 5 different places.**

**Done when.** The person can name the cause and the action that clears it.

**Known friction.**
- The bespoke "incident lock" wait is gone (#3086), but grant waits,
  capacity, approval, deferral, merge queue state and schedule
  skips are still each explained in a different place and voice.
- **Unclear**: whether a task held only by capacity (team worker slots,
  sandbox budget) says so on the task itself.

**How a walkthrough would check it.** Each cause can be produced in demo:
`-base-branch-ci` (red base), `-merge-queue-states`, `-agent-paused`,
`-scheduled-workflows` with an overlapping firing, and a team at 0 workers.
For each, the check counts the clicks from the task list to a sentence
naming the cause.

**Simplification ideas.**
- One "Why isn't this moving?" panel on every task that collects all the
  reasons in one voice, each with its action.
- The same panel for a schedule.

---

## 9. Release flow (operator)

**Who and why.** The operator ships the day's `main` as a candidate and
promotes the last candidate to `latest`.

**Starting state.** `main` and `candidate` with ports flowing, and the
nightly and live suites running on `candidate`.

**Steps today** ([release-candidate-management.md](release-candidate-management.md),
`.grain/schedules/release-candidate.yml`, `scripts/release-candidate-cut.sh`):

1. The 19:00 Pacific schedule files the cut task (`overlap: skip`).
2. The agent writes the candidate gate's verdicts file from the candidate's
   nightly and live runs.
3. The script back-merges `candidate` into `main`, pushed through
   `release-push-main.sh` (#3561, **on main**).
4. The gate decides: promote `v1.{X-1}.0` and `latest`, or hold.
5. Release notes go to `docs/releases/`.
6. `candidate` is fast-forwarded to `main` and tagged `-rc.1`.
7. `promote.yml` retags the images in grain CI.
8. The operator reads the cut's closing note. On a conflict or a hold, they
   act: review the reconciliation PR, Submit a security hold, or triage.

**Count: 1 human step on a clean day, 2–4 on a held or conflicting one.**

**Done when.** `latest` points at the new release, its images are retagged,
`docs/releases/v1.N.0.md` exists, and `candidate` carries the new `-rc.1`.

**Known friction.**
- *An overlap-skipped cut* (#3560, **fixed**: skips are now recorded and shown).
- *The back-merge push refused by the git proxy* (#3561 and #3546, **fixed on
  main**). It still depends on `candidate` (what the deployment runs)
  carrying the fix.
- A hold or a conflict surfaces as a task to read, not a state on the
  Releases page. The release watchdog (every 6h) predicts holds.

**How a walkthrough would check it.** The script runs against a local bare
repo (its own tests do). The gate is `scripts/candidate-gate.sh` against a
verdicts file. The UI half (the **Releases** tab and trains) is drivable on
demo with `-qualification`. Real tags and image retags need GitHub and GHCR,
so those should stay in the live cut.

**Simplification ideas.**
- Show the cut's outcome (promoted, held, or conflicted, and why) on the repo's
  Releases tab, so it doesn't have to be read out of a task.
- A one-click "triage as known/infra" for gate lines.

---

## 10. Set up standing work for a repo

**Who and why.** A repo owner wants recurring work: nightly CI, a weekly
review, a template on a schedule. Later they want to turn it all off.

**Starting state.** A repo on the deployment.

**Steps today** ([declarations.md](declarations.md), [schedules.md](schedules.md),
`RepoWorkOwnership.jsx`):

1. Choose where to declare it: a `.grain/workflows` job with `on.schedule`
   (runs CI, then files a review), `.grain/schedules` plus `.grain/templates`
   (files a task), or a settings-owned row in the UI or grain/config.
   **(decision)**
2. Write the YAML and open a PR. Any `.grain/` change needs a human Submit
   (`grain-declarations` trigger).
3. Merge, and the declarations are read.
4. Check the **Schedules** page for the declared rows and their next firing.
5. To turn it off: the repo page's **Standing work on this deployment**
   switch (#3332, **on main**), or per-row three-state overrides.

**Count: 5 steps, 1 decision with three answers.**

**Done when.** The schedule's next firing is listed, and the first firing
filed its task or run.

**Known friction.**
- The three ways to declare standing work look alike and behave differently.
  Swapping a workflow cron for a schedule plus template silently skips the
  CI run.
- Found in the code: the switch in `RepoWorkOwnership.jsx` passes
  `inputProps` twice, so the second (`role`/`aria-label`) replaces the
  first's `aria-describedby`, and the switch loses its description for
  screen readers. This is a small bug and isn't fixed here, because this
  task changes docs only.

**How a walkthrough would check it.** `grain demo -github-sim -fake-ci
-scheduled-workflows` reads a simulated repo's `.grain` declarations and
fires its workflow schedules. The walkthrough checks the Schedules page and
toggles the switch off and on. `ui-walkthrough-automation.yml` covers parts
of this.

**Simplification ideas.**
- A "New standing work" UI that writes the declaration PR for you and picks
  the right kind from the answer to "should this run CI?".
- Show on each schedule row what it will do (a CI run plus a review, or a
  task).

---

## 11. Navigate between teams (retired)

The team switcher, team URLs (`/t/<team>/...`) and per-team pins went with
the multi-tenant UI (single-tenant reset 3e-ii, task 4777). A grain has
one deployment, so there is nothing to navigate between.

---

## Summary

| # | Journey | Steps today | Worst friction point | Walkthrough can automate |
| --- | --- | --- | --- | --- |
| 1 | Host install and first sign-in | 14 (5 decisions, 5 shell) | Reading the generated password with `sudo cat` | UI part yes. Install needs a VM |
| 2 | A site's own installer | see the wrapper distribution's own docs | | |
| 3 | Upgrade and confirm version | 7 (2 decisions) | Host-mode unit isn't refreshed by an upgrade | Yes, with `-upgrade-image` (no restart) |
| 4 | Set up a new team (retired) | | | |
| 5 | GCP access | 2 (path A), 3 (path B needs the API) | Named keys have no UI | Path A yes. A real mint needs GCP |
| 6 | File a task and merge it | 6 to file + 5 to merge | About 20 controls on the new-task form | Yes, with sim GitHub, fake CI and e2e runs |
| 7 | Answer a question or chat | 2–3 | No feedback between reply and dispatch | Yes |
| 8 | Find out why it's stuck | 1–3, in 5 places | Reasons spread across five surfaces | Yes, each cause can be seeded |
| 9 | Release flow | 1 (clean), 2–4 (held) | Outcome lives in a task, not on the Releases page | Script yes. Tags and images need live |
| 10 | Standing work for a repo | 5 (1 three-way decision) | Three look-alike ways to declare it | Yes, with `-scheduled-workflows` |
| 11 | Navigate between teams (retired) | | | |

## Proposed nightly simplification suite

*A proposal for a follow-up task. Nothing here is built yet.*

- **One check per journey.** Journeys 1 (UI half), 3 and 5–8 and 10 become
  `ui-walkthrough-journey-<n>` templates in `.grain/templates`, using the
  existing walkthrough protocol and the demo flags named in each entry.
  Journeys 1 (install half) and 9 become scripted checks. These are a
  `setup.sh` run in a disposable VM and the cut script against a local bare repo. They could run as a
  `.grain/workflows` job, so they are real CI runs.
- **Each check records a fixed measurement file**,
  `docs/test-runs/journeys/<date>/<n>.json`, holding steps, decisions, shell
  steps, form fields touched, wall-clock time per step (model connection
  save, for example), and friction notes in a fixed vocabulary: dead end,
  needs shell, misleading banner, wait with no feedback, or undiscoverable
  control.
- **It compares with the previous night.** One summary template reads the
  last two nights' files and reports any journey whose counts went up or whose
  friction list grew.
- **It proposes rather than fixes.** For each regression, and for at most one
  standing simplification idea per journey per week, it files a focused
  "Simplify <journey>: <idea>" proposal (`propose_task`, needing approval)
  with the before and after counts. It never changes product code itself.
- **This document is the input.** Each check's template quotes its
  journey's section. When a simplification lands, the PR updates this
  document's steps and the summary table, so the counts that the suite
  compares against are reviewed by a human.
- **Cadence and cost.** One `.grain/schedules/user-journeys.yml`, nightly, on
  `main`, running a suite of the per-journey templates (`mode: count`,
  `count: 1`), and kept inside the nightly timeout budget.
