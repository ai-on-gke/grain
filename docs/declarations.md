# Declarations: a repository's own templates, suites and schedules

grain/task-60. A repository can declare the standing work that belongs to
it -- the templates its tasks are filed from, the suites that run them,
and the schedules that fire either -- as files in its own `.grain`
directory. Grain reads them from the repository and keeps what it holds
level with what the repository says.

This is `pkg/ci`'s argument for `.grain/workflows`, one layer up. A
repository's checks live in the repository because they describe that
repository and change when it changes; its nightly chore, its release
sweep and the template they both file from describe it just as closely,
and until now they could only be typed into grain's own settings -- a
deployment away from the code, invisible in the pull request that changes
the thing being scheduled, and impossible to review alongside it. A file
under `.grain` is reviewed where the code is reviewed, branches with it,
and is live the moment it merges.

Nothing here replaces grain's own settings. A template that holds a
capability, or one that belongs to the deployment rather than to any
repository, is still a row an operator writes, in grain's UI or as a pull request against the state
repository. What a repository declares is the subset that is genuinely
about that repository.

[grainfile-format.md](grainfile-format.md) describes how grain plans to
read the same declarations written in the Grainfile Format, which `settings:`
keys it reads, and the execution records it writes back.

## The files

One directory per kind, one file per thing, YAML. A file is `.yml` or `.yaml`
and its name -- its *stem* -- is the declaration's identity: a suite names
the templates it runs by stem, a schedule names its template or suite by
stem, and the row's id is derived from it (`model.RepoDeclaredID`). The
`name:` inside a file is prose for a listing and can be reworded freely;
renaming the file is what makes a new declaration.

```text
.grain/
  settings.yml
  review.yml
  templates/nightly.yml
  suites/sweep.yml
  schedules/nightly.yml
  triggers/design.yml
  merge-rules.yml
```

### `.grain/review.yml`

A repository can declare its default review prompt and review details in
`.grain/review.yml` (or `.grain/review.yaml`, or `.grain/templates/review.yml`).
Tasks targeting this repository automatically attach this review template if no
review template is explicitly chosen.

```yaml
name: Bug hunt                     # optional; defaults to "review"
title: Review the proposed change  # optional; defaults to "Review: <task title>"
prompt: |                          # instructions (alias: "body")
  Read the diff on this branch and look for bugs, security issues, and style problems.
network: none                      # optional; "open" or "none"
custom-fields:                     # optional; this deployment's declared fields
  team: security
```

### `.grain/settings.yml`

A repository can declare four of its own settings, in `.grain/settings.yml`
(or `.grain/config.yml`):

```yaml
prompt-extension: "Follow style."  # standing instructions, appended to the operator's
setup-command: "make init"         # setup shell, run after the operator's
port-branches: [main, candidate]   # paired branches; unioned with the operator's
port-types:                        # per direction, the commit types that are ported
  main->candidate: [fix, revert]
```

`port-branches` names the branches that are kept in step: when the merge
queue lands a change on one of them, grain files an approved port task
carrying it onto the others ([release-candidate-management.md](release-candidate-management.md) §5).
It is declarable because a port grants nothing new. It is the same change
somebody already approved, under the same team and capabilities, landing
through the same merge queue and CI. Leaving it out, as every repository
does by default, ports nothing.

`port-types` narrows a direction, written `<from>-><to>`, to the
conventional-commit types it ports: a change is ported that way only if
one of its own commits is `<type>:` / `<type>(<scope>):` (with or without
`!`; git's `Revert "..."` counts as `revert`), or carries a `Port: yes`
footer. A direction it does not name ports every merge. It suits a
release branch cut from another one, which needs only the fixes because
the cut brings the rest. It files only fewer ports, never more, and only
for a pair the repository declared itself in `port-branches`. On a pair
the operator configured, a declared list only adds types to an
operator's own `port_types`.

Four, out of the eighteen fields of a repository's settings row, and which
four is a decision with a rule behind it (grain/task-340). The rule is
**authority, not behaviour**:

> A setting is declarable when it changes what a repository's own work
> does, and never when it changes what that work is permitted to do, who
> judges it, or whether the repository is believed.

The tempting rule -- "a repository may declare anything that only affects
itself" -- is wrong, because by that test a repository could grant its own
tasks `gcp-key`, which affects nothing but its own tasks and is exactly
what must never happen. What makes the real rule safe is that a repository
*already* decides what code runs: `.grain/workflows` is in the repository,
its tests are in the repository, its setup script runs its own tree, and
every agent grain dispatches there can rewrite all three. Nobody is
protected from a repository's code by keeping a merely behavioural setting
out of the repository. What a repository must never decide is the
authority that code runs with.

So everything else is refused, by name and with its reason, for one of
four reasons -- the classification is `pkg/declschema`'s
`RepoSettingsSchema`, field by field:

| Refused | Why |
| --- | --- |
| `default-capabilities`, `capabilities`, `ci-capabilities`, `release-secrets`, `clone-url`, `repo` | **Authority.** What a task may do, which secret names a job may resolve, which tree grain clones. The capability model rests on these being an operator's word. |
| `ignore-github-ci`, `unfixable-checks`, `base-branch-ci-jobs`, `require-branch-up-to-date`, `watch-default-branch` | **Verdict.** Each decides whether this repository's checks are believed, or what the deployment spends reaching a verdict. A repository that can say its own failures do not judge it has exempted itself from review as surely as one that can grant itself a capability. |
| `declarations` | **Bootstrap.** The switch meaning "this repository's word is good enough to read" can never itself be something the repository says. Operators set it centrally, in `grain/config`. |
| `work-ownership`, `release-workflows-disabled` | **Ownership.** Whether *this deployment* runs this repository's standing work -- its schedules and its release. It is the one refusal here that is not about trusting the repository: several deployments may be looking at one repository, and no file in the repository can know which of them is cutting its releases. |

### Where ownership is said instead

Refusing the key in the repository leaves the question still to be
answered somewhere, and it is answered on this deployment, at two
levels: one default per repository -- does this deployment run this
repository's standing work at all -- and an exception per work item,
where a schedule's id is an item and the release is one more item.

All three states are real and none of them is the absence of the others.
"Nobody has said" follows whatever the rule says and moves with it;
"off" is a decision that survives the default being turned back on; "on"
is the decision that survives it being turned off. That is why the
surfaces for it show what was said and what it resolves to as two
separate readings, and why none of them is a checkbox:

- **Repos → a repository → Work ownership** lists the repository default
  and every item under it -- the release, and each schedule this
  repository declares or fires into here -- with what this deployment
  has said about each and what that comes to.
- `grain repo work-ownership <owner/name>` prints the same list.
  `-set on|off|unset` writes the repository default, and `-set … -item
  <id>` writes one item's exception to it. `unset` is not `off`.
- `GET`/`PUT /api/repos/{owner}/{name}/work-ownership` is the same thing
  over HTTP; the `PUT` body is `{"default": …}` or `{"item": …, "state":
  …}`.

A deployment whose settings are exported to a repository still has the
`work_ownership` and `work_overrides` columns of its `repo_config` row,
and they are the same two fields -- but a column cannot say which of a
repository's schedules this deployment is the one firing, which is what
the readings above are for.

### A requirement is not a grant

`ci-capabilities` and `release-secrets` are refused above as authority,
and that is the whole of what they now are: what this deployment is
willing to *give* this repository's CI jobs.

What a job *needs* is a different statement, and it lives in the
repository, on the job, next to the steps that cannot run without it:

```yaml
jobs:
  publish:
    capabilities: [package-publish]
    secrets: [NPM_TOKEN]
    steps:
      - run: ./scripts/release-images.sh
```

Writing `package-publish` there produces no credential. It states that
this job's steps cannot do their work without one, and the two meet
where the job would start: if this deployment does not provide that
capability for this repository, **the job does not run**, and its record
says `this job requires the capability "package-publish" and this
deployment does not provide it for acme/widgets`. Nothing is spent on it
-- no sandbox, no cache entry in either direction -- and the rest of the
suite runs, which is why the declaration is on the job rather than on
the repository.

Repository access is the one exception to "a requirement is not a
grant". A `repo-read:<owner>/<repo>` or `repo-write:<owner>/<repo>` on a
job *is* the allowance, and no `ci-capabilities` entry is needed or
consulted. The authority is the GitHub credential of the team the job
runs as, which the git proxy clones and pushes with, so GitHub refuses
whatever that team's token cannot do. Untrusted runs still never hold
write (docs/ci-capabilities.md, "Repository access").

A job that declares no `capabilities:` is handed every capability the
deployment provides, exactly as every job was before the key existed.
Repository access is not among them: it reaches only a job that
declared it. A
job that declares is narrowed to what it declared, so it no longer queues
for finite credentials it does not use.

Release secrets are the exception: a job that declares no `secrets:` is
handed **no** release secrets, on any event. A release or a
workflow_dispatch would otherwise place every signing key and publish
token the repository has configured into every job that answers it,
including jobs written on a branch nobody has reviewed (issue #2389). A
job that reads one names it. Runs grain starts on its own to warm the
sandbox guest cache carry no release secrets at all, whatever their jobs
declare: they run whichever branch first missed the guest, and filling a
cache never needs one.

A file that writes one of them is an error naming the key, with the reason
-- never a key that is quietly dropped. That is the whole point: a
silently ignored `capabilities:` is the failure mode this design exists to
avoid.

The two that are declarable **add to** what an operator already set for
the repository; they never replace it. `prompt-extension` is appended
after the operator's text and `setup-command` runs after the operator's
command, and there is no spelling that means "ignore the operator's". They
live in columns of their own (`repo_config.declared_prompt_extension`,
`declared_setup_command`) so that the settings boxes a person maintains
are never rewritten by an agent, so that a person can see which half came
from where, and so that deleting the file takes back the declared layer
and nothing else.

Adding a field to `model.RepoConfig` does not build until it is
classified, which is what keeps this list from rotting.

### What it is not: the whole row

Until 21 Sep 2026 this file *was* the repository's `repo_config` row.
Every field grain read from it was written on every sync, so a field the
file left out was a field set back to its zero value, and deleting the
file cleared the row. That made the first sync the sharp edge: a
repository an operator configured months ago handed its row over to a
file probably written without the row in front of whoever wrote it, and
everything the file did not restate went at once.

That is not a hypothetical. `bwsalmon/grain` had its declarations
switched on at 18:52 UTC on 20 Sep 2026 against a file naming three
fields, and the row lost its post-merge guest-cache dispatch, a CI
capability, its base-branch job list and `watch-default-branch` in one
poll; every branch's CI built its own ~1GB sandbox guest for the rest of
the day (grain/task-460, 464, 483).

A guard was written for the two worst of those losses and has been
removed with the reach that made it necessary. There is nothing left for
it to guard: a settings file cannot name a capability, a release secret
or the publishing switch, so it cannot take one back. What it writes now
is two columns of its own, `declared_prompt_extension` and
`declared_setup_command`, composed with the operator's where they are
read. The rest of the row is not in a declaration's reach at all, in
either direction -- a file that omits `setup-command` is a file that
declares no setup command, not one that deletes the operator's.

What it *does* write is said out loud (grain/task-476). A sync that
changes either of the two declared columns writes one line to the daemon
log naming each key the file names it by, with both values:

```
orchestrator: acme/widgets at 0123456789ab: .grain/settings.yml changes 2 settings field(s)
of this repository: prompt-extension: "Mind the generated client." -> "Mind the generated
client, and the fixtures."; setup-command: "make deps" -> (blank)
```

Neither of those can lose an operator anything any more, but both are
still changes nobody is told about at the time: the prompt is what every
agent dispatched here is handed and the setup command is what every
checkout of it runs, so a merge that edits the file shows up later as an
agent behaving differently, with nothing pointing at the commit. The line
is what makes "which merge changed this?" answerable, on System → Logs
beside the merge queue's own lines. Only a sync that actually changes
something logs one, so the polling that follows is silent.

Holding the first sync of an already-configured repository for an
operator to accept was the other half of grain/task-476, and it is
**closed rather than open**: the whole-row overwrite it would have
guarded no longer exists, and a diff of two columns that only add to what
an operator wrote is not worth a UI surface to accept it on.

### `.grain/templates/<name>.yml`

```yaml
name: Nightly dependency bump      # optional; the file's own name if left out
title: Bump dependencies           # required -- what the task is called
body: |
  Update every dependency to its latest patch release and run the tests.
base: main                         # optional; the repo's default branch if left out
auto-merge: true                   # optional; default false
network: none                      # optional; "open" or "none"
model: claude-opus-5-5             # optional; the team's default model if left out
interactive: true                  # optional; default false -- every task it files is a live chat
keep-running: false                # optional; default false
priority: 10                       # optional; positive integer (unset/0 files tasks at default 5)
custom-fields:                     # optional; this deployment's declared fields
  team: infra
  reviewers: [ana, bo]
fields:                            # optional; the inputs it asks for when filed by hand
  - name: environment              #   a `{{environment}}` in the title or body
    label: Which environment?      #   optional; the name if left out
    options: [staging, production] #   optional; a picker rather than a text box
    default: staging               #   optional; what an unattended firing uses
    required: false                #   optional; only asked of whoever fills the form
capabilities:                      # optional; capabilities and repository access
  - repo-read:acme/upstream        #   the filed task may fetch acme/upstream
  - repo-write:acme/downstream     #   ...and push its own branch to acme/downstream
  - gcp-key                        #   capabilities bounded by the filing team
```

#### Capabilities and repository access: `capabilities:` on a template or schedule

`.grain/templates/<name>.yml` and `.grain/schedules/<name>.yml` may declare
any capability the team is allowed to hold, as well as `repo-read:<owner>/<repo>`
and `repo-write:<owner>/<repo>` on templates.

**Team boundary.** A declared capability cannot grant what the filing team is not
authorized to have. The team the task runs under is the bound: each capability's
own `CapabilitySpec.AuthorizedTeams`, plus team-scoped capabilities (such as a
team-scoped `self-debug` and `pipeline-control`). A named GCP key, `gcp-key:<name>`,
is the deployment's since single-tenant reset stage 4e, and is dropped only when the
deployment has no key of that name ([GCP keys](team-gcp-keys.md)).
An admin-only capability cannot be given to a non-admin team's task.
When a task is filed (by hand, schedule firing, or path trigger) from a declaration
requesting unauthorized capabilities:
- Unauthorized capabilities are filtered out.
- A refusal note is recorded on the task thread (`TeamUnauthorizedRefusal`),
  naming the stripped capabilities and the filing team.

**Repository access.** For repository access on templates,
`repo-read:<owner>/<repo>` and `repo-write:<owner>/<repo>` are allowed:
- Each `repo-read:` becomes one of the template's `reads` and each
  `repo-write:` one of its `writes` (default branch). Every task filed from
  the template carries them, so the git proxy lets that task's sandbox fetch
  the repositories it reads, and push its own task branch to the ones it writes.
- The proxy clones and pushes a task's work with the filing team's own credential
  (`Store.TaskGitCredential`).
- The repositories named must be within the template's team's `target repos`.
  Otherwise the declaration sync is refused.
- Grain's configuration repository (`grain/config`) is refused in
  declarations. There is no other: a `grain/config-<team>` name is an
  ordinary repository name, which nothing serves.

**Schedules.** Schedules may also declare capabilities in `capabilities:` (for example,
capabilities needed specifically when firing this schedule). In a schedule firing,
capabilities from the schedule and the fired template are composed together.
Repository access (`repo-read:`/`repo-write:`) belongs on the template it fires
rather than the schedule.

**Trigger protection.** Because `.grain` definitions can declare permissions,
changes to `.grain` files are protected by repository-owned path triggers
(`.grain/triggers/*.yml`). Repositories define a sensitive gating trigger covering
`.grain/**` (with `gate: true`, `human: required`, `sensitive: true`) so that
pull requests modifying `.grain` declarations require human review and approval.

**Team approval.** Declared access needs no approval beyond what the work
already gets. The task files as its template would file it: approved when
a schedule or suite files it, and decided by whoever files it by hand.
The access only takes effect from the default branch, so the authority
behind it is the authority to merge `.grain/templates`. It is guarded the
way `.grain/workflows` is (see "Sensitive paths" below for the
recommended trigger).

**Proposals.** A proposal from such a task is held to the existing
inheritance rules (`model.UngrantedReads`, `model.UngrantedWrites`). It
may carry the reads and writes its parent task holds and still be
auto-approved, or auto-merge where its parent could. A proposal asking for
any repository its parent does not hold is filed for a person, with a note
naming it. Nothing about how a proposal inherits changes. The declared
access is simply part of what the parent task holds.

#### Refs beyond the task's branch: `push:` on a template

grain/task-2536. Every run may push its own task branch and nothing else:
not the default branch, not another task's branch, not a tag. A template
whose whole job is to move other refs -- a release cut -- says which ones
under `push:`, and the git proxy lets the runs of tasks filed from it push
exactly those, in the repository the template is declared in:

```yaml
# .grain/templates/release.yml
title: Cut a release
push:
  - refs/heads/main                # a fast-forward only
  - ref: refs/heads/candidate      # may be force-moved
    force: true
  - refs/tags/v*                   # created; an existing one is not moved
  - ref: refs/tags/latest
    force: true                    # moved on every release
  - ref: refs/heads/scratch-*
    delete: true                   # may also be deleted
```

**The declaration is the allowance, and the team's git credential is the
authority** -- the same rule as `capabilities:`. There is no team setting
and no admin grant behind it:

- **Explicit refs only.** Each entry is a full ref under `refs/heads/` or
  `refs/tags/`. `*` and `?` glob within one path segment (`path.Match`), so
  `refs/tags/v*` is every tag starting with `v` and `refs/heads/release/*`
  one level of branches under `release/`. A pattern that would match every
  branch or every tag (`refs/heads/*`) is refused: spell out what you mean.
  Other glob syntax (`[...]`, `\`) and malformed ref names are refused, and
  so is any key in an entry besides `ref`, `force` and `delete`.
- **Force and delete only where declared.** A deletion needs
  `delete: true`. Moving an existing tag, or moving a branch anywhere the
  proxy cannot show is a fast-forward, needs `force: true`. The proxy
  shows a fast-forward from the commits the push itself carries, walking
  them back from the new tip to the old one. Where that walk leaves the
  pack -- at once, for a push moving a branch to a commit the remote
  already has, which carries no commits -- it carries on through commits
  the proxy fetches from the repository itself, with the credential the
  push goes out with. So a commit pushed to the task's own branch first
  (to run CI on it there) can then fast-forward a declared branch.
  Creating a ref that does not exist yet needs neither.
- **This repository only.** The push set applies to the repository the
  file is in. A push of a declared ref to any other repository the task
  may write is refused.
- **Read from the default branch, never from the run's own.** The
  declaration sync reads `.grain` at the default branch's head and keeps
  the push set beside the declaration's provenance. The proxy reads it
  from there each time a run pushes, so a run cannot widen its own set by
  editing the file on its task branch, and a change to `push:` takes
  effect only once it merges. Narrowing it takes effect for runs already
  going.
- **Which tasks.** A task filed from the template carries it: by a
  declared schedule, by a suite, by a qualification plan or path trigger,
  or by a person instantiating the template on their team. The task
  records the template it was filed from, and the push set is looked up
  through it. A proposal an agent files from the template does not, so a
  run cannot hand its push set on.
- **Refusals say why.** A refused push names the ref, whether it was
  undeclared or a force-push or deletion its entry does not allow, and the
  declaration file to change.
- **Audited.** Every update of a ref other than the run's own branch that
  the proxy lets through is written to its audit log on its own line, with
  the ref, the task, the template and the declaration file.
- **Reviewed.** `.grain/` is on grain's trust-boundary list, so a change
  that adds or widens `push:` is held for the security review, which is
  told to look for exactly that.

`grain declarations check` prints each template's push set; the repo page
shows it in the declarations status, and a task's page shows the push set
it was filed with as "May also push". A `.grain/workflows` job needs none
of this: a job holding `repo-write:` for a repository may already push any
ref of it (`docs/ci-capabilities.md`), and has no task branch to be held
to.

The repo server's any-ref grant, `https://grain.dev/capabilities/repo#write` with `{"force": true, "refs": ["refs/heads/*", "refs/tags/*"]}`
(formerly the bare `git-push-any-ref`, which is still read as it), is
still there for ad-hoc work with no template behind it. It is a team
grant, not an admin one: a team grants it to its own task, and the
team's own credential still bounds every push.

### `.grain/suites/<name>.yml`

```yaml
name: Nightly sweep
templates: [nightly, docs]         # by file stem, from this repo's own templates
mode: count                        # "count" (default) or "until-clean"
count: 2                           # count mode; default 1
max-passes: 5                      # until-clean mode; default 5
require-approval: false            # optional; default false
auto-merge: true                   # optional; default true
```

### `.grain/schedules/<name>.yml`

```yaml
name: Every night
template: nightly                  # a declared template, by stem...
# suite: sweep                     # ...or a declared suite...
# workflow: nightly                # ...or a .grain/workflows file, by stem or path; exactly one
base: main                         # optional
enabled: true                      # optional; default true
overlap: skip                      # optional; "skip" (default) or "allow"; "skip" only with workflow:
                                   # "skip" holds a firing that overlaps the last one and fires it once
                                   # when that closes; every skip is recorded (see "Skipped firings")
review: on-failure                 # optional, workflow: only; "on-failure" (default) or "always"
model: gemini-3.1-pro              # optional; beats the template's own; refused with suite:

# Exactly one cadence:
every:                             # every N hours from the last firing
  hours: 6                         # or `minutes: 30` -- at least 10, never both
# daily:
#   at: "09:00"
# weekly:
#   day: monday
#   at: "09:00"
# monthly:
#   day: 1
#   at: "09:00"
```


#### Skipped firings

`overlap: skip` never drops a firing on its own. When an occurrence comes
due while the previous firing is still open, grain leaves the schedule's
next run where it was and fires that occurrence once, on the first poll
after the previous firing closes. If the next regular occurrence comes
round first, it is folded into the same single late firing. The release cut
(`.grain/schedules/release-candidate.yml`) relies on this: a cut whose
predecessor merges at 02:09 runs at 02:09, not the next night. There is
no separate catch-up setting because this is what `skip` already does.

Every occurrence that came due and filed nothing is recorded, once
however many polls re-skipped it, with the reason, the open task and its
state when there is one, and what became of it: still held, fired late
(with the task it filed), or dropped. The reasons are the previous
firing still being open (task, suite run, workflow run or review), the
schedule being paused, its team being archived, its repository being off
the target list, or that repository's standing work being off. The last
two drop the occurrence instead of holding it. The daemon logs one line
per recorded skip, for example `schedule repo:schedule:… skipped at
2026-10-13 02:00 UTC: #3201 from the previous firing is still open
(awaiting_submit); … is held`. The schedules page shows the newest skip
on the row and the history in the schedule's pane (`ui.Schedule.skips`).

### `.grain/triggers/<name>.yml`

grain/task-1712. Run a template on every merge, or every pull request,
that touches a set of files and folders.

```yaml
on: merge                  # merge | pull_request
branches: [main]           # optional; the default branch if left out. Globs allowed (rc/v*)
paths: [docs/design/**]    # required; directory prefixes, or globs with ** for any depth
ignore: ["**/*.lock"]      # optional; a change only to ignored paths does not fire
template: design-planner   # required; a stem from .grain/templates, or a deployment template id
per: change                # change (default): one task per merge range / per pull request
                           # folder: one task per changed top-level file or folder
                           #         directly under the matched path's literal prefix
gate: false                # pull_request only: hold the change's merge until the task is done
human: on-findings         # gate only: required | on-findings (default) | no
auto-approve: false        # whether the filed task lands approved, with no human
```

A path with no glob characters matches itself and everything under it
(`docs/design` matches `docs/design/foo/README.md`). Otherwise each
`/`-separated segment is a `path.Match` glob, and a segment that is
exactly `**` matches any number of segments, none included. With
`per: folder`, `docs/design/**` groups `docs/design/foo/README.md` under
`docs/design/foo` and `docs/design/index.md` under itself.

A file is refused, the way any other declaration is, if it has no
`paths`, a glob that does not parse, a template that is not there (a
deployment template id is checked against this deployment's templates at
sync time), `gate` on anything but `on: pull_request`, or `human` without
`gate: true`. A refused file keeps the last good commit's triggers running.

**Rules come from the base, never the change.** Triggers -- and the
templates they name -- are read only from what grain synced off the
default branch, so a pull request cannot add, edit or delete the trigger
that judges it. Every filed task records the base commit its rule came
from (in its body and a `path-trigger-rule:<sha>` tag). A trigger
watching a branch other than the default branch is still the default
branch's copy of the rule.

**`on: merge`.** Each cycle grain reads the head of every branch a merge
trigger watches, and diffs it against the head it last processed for that
repository and branch (persisted, so a restart neither re-fires nor
skips; a branch's first look only starts the watch). A burst of merges
between two looks is one diff. The task is filed against that branch,
with `{{trigger}}`, `{{paths}}` (the matched files, one per line),
`{{group}}`, `{{from_sha}}`, `{{to_sha}}` and `{{branch}}` substituted in
the template's title and body, and the same facts appended to the body.

**`on: pull_request`.** A change grain dispatched is judged when its run
opens or updates the pull request, and again whenever the merge queue
finds its head at a commit it has not judged, against the pull request's
files (its diff from the merge base). Variables: `{{trigger}}`,
`{{paths}}`, `{{group}}`, `{{pr}}`, `{{head_sha}}` and `{{base_sha}}`
(the base commit the rule was read at). Without `gate`, the task is an
ordinary one in the repository. With `gate: true` it is a review stacked
on the change's branch (with a branch and pull request of its own that
merge back into the change's -- unlike grain's own trust-boundary security
review, which pushes its fixes to the change's branch directly), the
change's auto-merge is withdrawn, and the
change and its pull request say which trigger demanded what. The hold is
released the way the security hold is: `on-findings` once every gating
task has finished with no findings and any fixes it pushed have merged
back into the change's branch, `no` once every one has finished whatever
it said, and `required` never -- that one waits for a human's Submit,
however clean its reviews were. A gating task that left findings holds the
change as questions to its owner, and the owner's reply sends another run
to carry the decision out (docs/implementation-notes.md, "What a review
does with what it finds"). Several gating triggers matching one change
all have to finish, under the strictest `human` among them. A change whose
file list cannot be read in full is held by every gating trigger.

**One runnable task per key.** A key is (repository, branch, trigger,
group) for a merge and (pull request, trigger, group) for a pull request.
A new firing for a key whose task has never been dispatched supersedes it
(a merge keeps the earliest `from_sha` and every file since); one whose
task is running, parked or has a pull request open is filed depending on
it.

`docs/design-docs.md` is a worked example: a trigger and template pair
that plans merged design-doc changes into implementation and test tasks,
and reads what is already built from the `Design-Item:` trailers on the
branch rather than from any file of its own. (bwsalmon/grain has that trigger turned
off for now; the doc says how to file the planner by hand.)

#### Sensitive paths

grain/task-1714. A gating trigger can mark the paths it watches as
*sensitive*, and say who has to release a change to them:

```yaml
on: pull_request
paths: [payments/**, config/keys]
gate: true
sensitive: true
human: required                   # required | on-findings (default) | no
approvers: [team:payments, alice] # optional; default: the deployment's owner
# template: grain's security review if left out; a stem, a deployment
#           template id, or `none` (no automated review -- human: required only)
```

- **Review.** The template defaults to grain's own security review
  (`system:security-review`). A sensitive path needs at least one kind of
  review, so `template: none` is refused unless `human: required`. If a
  template the trigger names has gone by the time it fires (a deployment
  template deleted since the sync), grain files its security review in
  its place.
- **Approvers.** Under `human: required`, a clean review does not release
  the change: a person has to press Submit. Whoever holds the access code
  is the deployment's owner ([access-code.md](access-code.md)), and a
  single-tenant grain has no accounts or team members, so a person's
  Submit approves every outstanding trigger, whoever its `approvers`
  name; `approvers` records who the repository meant. Auto-merge comes
  back once none is left. A Submit, or an edit turning auto-merge on,
  from an agent -- a run's token, a sandbox -- is refused with a 403.
  Approvals are tied to the head they were given at: a push that moves
  the head re-judges the change and clears them.
- **Checked at sync.** Every `team:` approver has to be a team this
  deployment has (the default one, by id or name), and every template
  has to exist. Otherwise the sync is refused like any other bad
  declaration.

**The declarations guard themselves.** Grain enforces these rules itself,
and no `.grain` file can turn them off:

- A change touching `.grain/triggers/**` is grain's built-in
  `path-triggers` category (`pkg/trustboundary`, every repository). It is
  held with `human: required`, and only a person may release it.
- So is a change touching a template that one of the base's gating
  triggers files (`trigger-templates`).
- Grain's other built-in categories keep applying to every repository:
  `hold-policy`, `ci-suppressions`, and `grain-config` for the rest of
  `.grain/`. The rules in force are grain's categories plus the
  repository's own triggers. A repository can add categories or tighten
  review, never remove either.
- The deployment's own state repository (grain/config, or the external
  config repository it reads) is also judged as state: a change that
  grants reach, binds a secret or sets how a sandbox runs (templates,
  suites, schedules, `grain_config`, `repo_config`, `repo_workflow`,
  standing and persistent sandboxes, GCP keys, scripts, workflows) is
  `state-privilege`. It is held with `human: required`, because a merged
  change there is applied to the running daemon on its next tick. Only
  boards, custom fields, template inputs, memory and release notes merge
  themselves (`trustboundary.ClassifyState`, which fails closed for any
  other path).
- A hold records the repository rules it was decided under
  (`repo-rules@<base sha>:<hash of the triggers>`) beside
  `trustboundary.Version`. A clearance is pinned to the same hash, so a
  change cleared before a trigger began guarding one of its paths is
  classified again before it merges.

**Failing closed.** If the base's trigger files do not parse, or name a
template or approver that does not exist, the last good triggers keep
running as before. In addition, every self-merging change in the
repository is held for its owners (`unparseable-triggers`), and the hold
says why. Once the base parses again, a change held only for that is
judged again, at the head it was held at, under the fixed triggers and
grain's own categories. If they hold nothing, it merges itself again;
if they hold something, it waits for that instead. The fix does not
release a change in these cases, which stay with an owner's Submit:
- another owner-release category held it too;
- its head has moved since it was held;
- a review of it left findings;
- an owner already decided on it.

A repository with no `.grain/triggers` behaves exactly as it did before.

**Where it shows.** The held task's pane and the pull request's note list
each matched trigger or category, where it came from (the repository's
file or built in), the reviews it filed, and who can release it. The
task API carries the same information as `approvals`,
`pathTriggerHold`, `pathTriggerTasks` and `repoRules`.

**Recommended:** mark `.grain/workflows/` sensitive with
`human: required`. A workflow's capability declarations grant repository
access (task 1654), so a change to one is a change to what CI may do.
For the same reason, mark `.grain/templates/` and `.grain/schedules/`
sensitive too. A template's `capabilities:` grants its tasks repository
access (task 1780), and a schedule decides that such a template runs
unattended:

```yaml
# .grain/triggers/declared-access.yml
on: pull_request
paths: [.grain/workflows, .grain/templates, .grain/schedules]
gate: true
sensitive: true
human: required
```

### `.grain/merge-rules.yml`

Per-path merge policy (grain/task-1659, grain/task-1660): which CI system
gates a merge, which grain CI jobs and GitHub checks must pass, whether the
change may merge itself, how long the merge queue waits on CI before it
hands the change to a person, whether it may be batched with others, how
many automatic repairs it gets, whether a failing job is rechecked for a
flake, and whether a merge is ported to the paired branch. One file per repository. `.grain/sensitive.yml` -- the
name an earlier plan gave it -- is read too; if both exist,
`merge-rules.yml` is read, the other is ignored, and the sync logs a
warning.

```yaml
# .grain/merge-rules.yml
defaults:                      # every file no rule matches, and the PR as a whole
  stall_timeout: 2h
rules:
  - id: docs
    description: Documentation only needs lint.
    paths: ["docs/**"]
    ci:
      grain: [lint]
  - id: ui
    paths: ["ui/**"]
    ci:
      grain: ["ui e2e"]
  - id: deploy
    paths: ["deploy/**"]
    ci:
      mode: both
      github: [build, cla/google]
    auto_merge: never
    stall_timeout: 30m
```

Each rule has an `id` (lower case, unique), an optional `description`,
`paths` (the same globs as a trigger's), and any of these sections, which
`defaults:` may also hold:

| Key | Values | Meaning |
|---|---|---|
| `ci.mode` | `grain`, `github`, `both` | Which CI system gates the merge for these paths. |
| `ci.grain` | job ids, job names or workflow names | Grain CI jobs that must pass. Not given: the full suite. |
| `ci.github` | check names | GitHub checks that must be present and green. Not given: every check on the commit. |
| `auto_merge` | `allowed`, `never` | `never` withdraws auto-merge: a person must press Submit. |
| `stall_timeout` | a duration (`30m`, `2h`) | How long CI may stay pending before the queue gives up waiting. A stalled grain CI run is retried once (the deadline is extended if the run is still making progress, otherwise a fresh run is started) and then handed to a person. A GitHub check that stays pending (a CLA bot, a `ci.mode: both` check like `cla/google`) is not retried: the change goes to a person at the first stall. It can only shorten the deployment's own deadline (2h). |
| `batching` | `alone`, or a maximum batch size (`1` or more) | `alone`: never batched with another change, so a risky change can't take a batch down or be bisected out of one. A number caps the size of any batch the change is in, on top of its team's own merge batch size. |
| `repairs` | `0`, `1`, `2` | The most automatic repairs the merge queue sends the change on. `2` is the queue's own maximum, and the second is still only given when something changed since the first (new commits on the base, or different failures). `0`: a failure goes straight to a person. |
| `flake_recheck` | `true`, `false` | Whether a failing grain CI job gets one uncached recheck before the change is blamed. `false`: a flaky-looking failure is treated as real. A merge batch with any such member is bisected without a recheck. |
| `port` | `true`, `false` | Whether a merge onto one of the repository's paired branches is ported to the other automatically. `false` files no port task; one can still be filed by hand. The task's own opt-out (`no_port`, or a `Port: no` footer) applies as well. |

**How rules combine.** A pull request's effective policy is the strictest
combination of `defaults` and every rule its changed files match:

- `ci.mode`: the repository's own CI mode (`ci_mode` / `ignore_github_ci`
  in its settings) is the floor, and rules only add to it. `grain` plus
  `github` is `both`, and `both` beats either one.
- `ci.grain` and `ci.github`: the lists are unioned. A rule that names no
  list asks for everything (the full grain suite, or every GitHub check),
  and that wins. So grain CI is *narrowed* -- `docs/**` needing only
  `lint` -- only when every matched rule names its jobs and no file falls
  to `defaults` without them. When it is narrowed the full suite still has
  to run against the exact head commit; only failures outside the named
  jobs are ignored, and a named job missing from the run is not a pass.
- `auto_merge`: `never` beats `allowed`. It sits alongside the security
  hold: either one withdraws auto-merge.
- `stall_timeout`: the shortest wins.
- `batching`: `alone` beats any number, and otherwise the smallest size
  wins.
- `repairs`: the fewest wins.
- `flake_recheck` and `port`: `false` beats `true`. Any one opt-out matched
  is enough.

`defaults:` contributes its `ci.grain`/`ci.github` lists only when some
changed file matched no rule. Everything else it says (`ci.mode`,
`auto_merge`, `stall_timeout`, `batching`, `repairs`, `flake_recheck` and
`port`) always applies, so a `defaults:` value is a floor that a rule can
only make stricter: `port: true` on a rule cannot undo `port: false` in
`defaults:`. A pull request whose file list can't be read
in full matches every rule.

The merge queue classifies each head commit once. It records the policy on
the task (the `mergePolicy` field in the task API) and leaves a note saying
which rule caused each requirement. `query_merge_queue` shows the same list
under each queued pull request. If a required GitHub check never appears on
the commit, the queue row names it and the rule that asked for it.

A change in a repository with rules is not batched until it has been
classified for its current head (the next queue cycle does that). Batching,
flake rechecks and porting act on the policy last recorded on the task.

**A worked example: bwsalmon/grain.** The orchestrator is where a bad
change does the most damage, so it is never batched and gets one automatic
repair rather than two. Documentation is ported to the paired branch and
batched as usual:

```yaml
# .grain/merge-rules.yml
rules:
  - id: orchestrator
    description: The merge queue and dispatcher -- merge alone, one repair.
    paths: ["pkg/orchestrator/**"]
    batching: alone
    repairs: 1
  - id: docs
    description: Docs follow the code to candidate.
    paths: ["docs/**"]
    port: true
```

A pull request touching `pkg/orchestrator/sync.go` and `docs/foo.md` is
then classified as:

```
- `batching: alone` -- from rule `orchestrator`
- `repairs: 1` -- from rule `orchestrator`
- `port: true` -- from rule `docs`
```

It merges on its own, never in a batch. If it breaks, it gets one automatic
repair ("automatic repair 1 of 1") and then goes to a person. Once merged,
it is ported. A docs-only change keeps the deployment's batching and both
repairs.

**Guarded like the triggers.** The rules are read from the base branch, so
a pull request is judged by the rules it is trying to change. Edits to
`.grain/merge-rules.yml` (or `.yaml`, or `.grain/sensitive.yml`) fall under
the built-in `merge-rules` trust-boundary category
(`trust-boundary/v4`): `human: required`, released only by the repository's
owners. A base whose rules file fails to parse holds every self-merging
change in the repository until it's fixed, and the fix releases them,
just as it does for a broken trigger.
The file can only add requirements. It cannot take away grain's built-in
categories or the full-suite pass that grain CI requires.

**Validation.** Unknown keys, an unknown `ci.mode` or `auto_merge`, a bad
glob, a duplicate `id`, a `stall_timeout` under a minute, a `batching`
that is neither `alone` nor a positive number, `repairs` outside 0-2, and
a `flake_recheck` or `port` that is not `true`/`false` are errors, and
the sync keeps the last good commit's rules. A `ci.grain` name that no job,
job name or workflow in `.grain/workflows` declares is a warning in the
daemon log. GitHub check names can't be checked against the tree, so a
misspelt one shows up instead as a required check missing on the queue row.
A rule's `review:` section is an error: per-path reviews are sensitive
triggers (above).

### `team:` on a template, suite or schedule

Any of the three may still say `team: default`, or leave the key out,
and nothing is kept of it: a deployment has no team besides the default
one, and schema 25 dropped the team columns (single-tenant-reset.md,
6-ii-b5). A file naming any other team is refused when it is read
(`repodecl.checkTeam`), which refuses the whole sync the way a file that
does not parse does: the last good commit's declarations keep running and
the reason is recorded against the repository. A declaration's
repository has to be one the deployment's target repos allow, and so
does every repository a declared template's capabilities reach, or the
sync is refused the same way (`model.checkDeclaredTargets`).

`auto-merge`, `require-approval` and `auto-approve-proposals` stay
declarable. Each decides only what happens to work in this same
repository, and a declaration takes effect only from the default branch,
so writing one already needs the authority to merge there -- the
authority these keys hand out.

Times of day are read against the deployment's own wall clock
(`Config.TimeZone`); there is no per-schedule timezone here for the same
reason there is none anywhere else (`model.Recurrence`).

### `.grain/workflows/<name>.yml` -- a workflow's own cron

grain/task-344. A repository declares a fourth schedule without naming
one, whenever a workflow in grain's own CI directory says when it runs:

```yaml
# .grain/workflows/nightly.yml
on:
  schedule:
    - cron: "11 2 * * *"
```

That cron becomes a task schedule, exactly as though somebody had written
one: the declared id namespace (`repo:schedule:acme:widgets:workflow.nightly`),
a provenance row pointing at the workflow file, read-only in the UI, and
gone when the cron is. What it fires is one of grain's own templates --
`system:workflow-review:<workflow>` -- and what that does is run the
workflow and report on the run. grain starts the run when the schedule
comes due and files the review once that run has *finished*, with the run
and its verdict in the task's opening comment (grain/task-351, and
`docs/schedules.md` on why the review waits for the run rather than
watching it); the agent reads the workflow file out of its checkout for
the job list, reads the failing jobs' logs, and finishes with a comment
and whatever follow-up tasks the failures earn.

So there is no cron in `pkg/ci`, and there was deliberately never going to
be one. A workflow cron is a recurring claim on this deployment's agents
and sandboxes, and this deployment has one scheduler for those, which an
operator can see, pause and read the firing history of. What was wrong
with the previous state was not the missing timer: `on: schedule` parsed
carefully -- a non-list, an empty list, an entry with no `cron` were all
refused -- and was then dropped on the floor, so a perfectly-formed cron
was accepted and never ran. This repository's own nightly suite had never
run once.

**Not every cron translates.** A task schedule has four cadences and cron
is strictly more expressive, so a cron is refused at parse time unless it
says exactly one of them:

| cron | becomes |
| --- | --- |
| `11 2 * * *` | daily at 02:11 |
| `0 9 * * mon` (or `* * 1`) | weekly, Monday at 09:00 |
| `0 9 1 * *` | monthly, the 1st at 09:00 |

Everything else -- `*/15 * * * *`, `0 9 * * 1-5`, `0,30 9 * * *`, `11 * * * *`,
a month other than `*`, a day-of-month and a day-of-week together -- is an
error naming the key and listing those cadences, and the repository keeps
whatever it declared before. Nothing is approximated: a suite somebody
believes runs every fifteen minutes and actually runs hourly is the same
silent wrongness as one that never runs, and harder to notice.

Two more things worth knowing:

- **The cron is read on the deployment's clock, not in UTC.** GitHub reads
  a workflow cron in UTC; a grain schedule is read against
  `Config.TimeZone` like every other schedule here, and a translated cron
  is a grain schedule. A deployment running in UTC sees no difference.
- **`.github/workflows` crons are left alone.** GitHub's own scheduler
  already runs those, and translating them too would run every nightly
  twice. Only grain's own `.grain/workflows` is read this way.

#### Which runs are reviewed: `review:`

grain/task-3160. A run is the check: a firing whose run passed -- it
concluded success, and at least one job in it passed -- files no review
task. It shows on the schedule's row and pane (its last run, branch,
conclusion and a link to the CI run) and on the CI page, labelled as the
schedule's. Every other ending files the review as before: a failure,
with the failing jobs in the task's opening comment; and an errored or
cancelled run, one lost with its daemon, one grain could not start, one
grain stopped waiting for, or a "success" with no job in it, because
nothing was tested in any of those.

To have every night reviewed, green or not, say so on the cron entry,
beside `timezone:`:

```yaml
on:
  schedule:
    - cron: "11 2 * * *"
      review: always       # "on-failure" (the default) or "always"
```

A `.grain/schedules` file with `workflow:` takes the same key at the top
level (`review: always`), and a settings-owned schedule row the same
`review:` key. Any other value is refused, and so is `review:` on a
schedule that runs a template or a suite -- those file their task
whatever happens.

#### Running a workflow on another branch: `workflow:` on a schedule

grain/task-3159. A workflow's own cron names no branch, so it runs on the
default one. To run the same workflow on another branch as well -- a
release candidate's nightly, say -- a schedule file names it:

```yaml
# .grain/schedules/nightly-candidate.yml
workflow: nightly          # .grain/workflows/nightly.yml; a stem, a file name or a path
base: candidate
daily:
  at: "14:11"
overlap: skip
```

What it stores is what the cron's own schedule stores, `base:` aside: its
template is `system:workflow-review:nightly`, so it fires down the same
path -- grain records a firing (`ScheduledWorkflowRun`, with `Branch` set
to `base:`), starts a trusted schedule run of `.grain/workflows/nightly.yml`
on that branch (`ci.Request.TrustedSchedule`, with the repository's
`ci_capabilities`, exactly as a cron's run gets), and files the review once
the run has finished. Never an agent standing in for the run. The review is
filed on `base:` and its title ends with the branch (`Review last night's
"nightly" workflow run on candidate`), as is the schedule's name unless the
file gives one, so the default branch's review and this one can be told
apart. Its id is the file's (`repo:schedule:acme:widgets:nightly-candidate`),
which is what `work_overrides` names.

It is refused, naming the reason, when the workflow does not exist under
`.grain/workflows`; when it has no `on: schedule` of its own (grain starts
these runs as schedule events, and a workflow that does not answer one has
no jobs in the run -- so the workflow still runs on the default branch
too); when it is kept as `.yaml` rather than `.yml` (the run is narrowed to
`.grain/workflows/<stem>.yml`); with `template:` or `suite:` beside it; and
with `overlap: allow`, because grain keeps one record of a workflow firing
per schedule and reviews one run at a time. A `base:` that does not carry
the workflow file is a run with nothing in it, reported as such by the
review. `auto-approve-proposals` defaults to true, as a cron's does.

A schedule carries no title or body of its own: a schedule is a cadence
and a template is the work, so a file that tries to be both is refused.
Unknown keys are refused too, naming the key -- a misspelt `auto_merge`
is an error rather than a setting that silently did nothing.

## Checking one before you push

```console
$ grain declarations check
/home/ana/widgets, read as acme/widgets (from origin)

template nightly  .grain/templates/nightly.yml
  id:         repo:template:acme:widgets:nightly
  ...
schedule nightly  .grain/schedules/nightly.yml
  fires:      suite sweep
  cadence:    every 6h
  next:       Fri 2026-09-18 13:29 UTC
              Fri 2026-09-18 19:29 UTC
              Sat 2026-09-19 01:29 UTC
```

`grain declarations check [dir]` reads a working tree and prints what it
declares, or prints every problem with it and exits non-zero. It talks to
no daemon and needs no `-server`: it is `pkg/repodecl.Load` over
`os.DirFS`, which is the same function the poll below uses, so the
verdict here is the verdict there.

That makes it a step a repository can run on itself:

```yaml
# .grain/workflows/tests.yml
- name: declarations
  run: grain declarations check
```

A declaration that does not parse then fails the pull request that
introduces it, rather than the poll a couple of minutes after it merges.

What that step needs is the CLI on `PATH`, and a sandbox does not carry
one today: this repository's own workflow would write `go run
./cmd/grain declarations check`, and any other repository has to fetch a
`grain` binary in a step before it. Putting one in the sandbox image is
what would make the three-line step above true as written.

Two things do not come from the tree, because of what they decide:

- **The repository.** Every declared id is derived from it, and it is the
  target of every task a declaration files -- which is why no file gets a
  say in it. `-repo owner/name`, or the checkout's own `origin` remote,
  which is what a CI job in a cloned repository already has.
- **The zone.** A time of day is read against the deployment's own wall
  clock (`Config.TimeZone`), which a checkout cannot see. `-zone
  Europe/London` previews against one; UTC is the default.

`-next N` says how many of each schedule's upcoming firings to work out
(three by default, `0` for none), through the same `model.Recurrence.Next`
the reconciler fires by. `every: {hours: 6}` and a daily cadence are easy
to write and hard to picture, and the firings are counted from now the way
a freshly-read schedule's are. `-json` prints the same reading as an
object with an `ok` field, for anything that wants to read it.

Nothing it can say switches the repository on: a tree that reads
perfectly still declares nothing until an operator turns declarations on
for that repository, below.

## What a declaration may not say

These files are in a repository that every agent grain dispatches there
can rewrite. So a declaration says **what work to do in this repository,
and when** -- never what that work may touch:

- **The target is always the declaring repository.** `repo:` and
  `target:` are refused. A declared template cannot file a task against
  anything else.
- **No `reads:` and no `writes:`.** A declared task sees the repository it
  is filed against, and only the other repositories its template names
  with `capabilities: [repo-read:..., repo-write:...]` (grain/task-1780,
  "Repository access" above). Those are fetched and pushed with the
  team's own GitHub credential, so they reach no further than that
  credential does. Nothing else reaches past the declaring repository.
- **No `grants:`, and no capability past the team's.** Capabilities
  are the deployment's to hand out (`Config.DefaultCapabilities`,
  `RepoConfig.DefaultCapabilities`, each capability's `AuthorizedTeams`).
  A template's or schedule's `capabilities:` may ask for one, and the
  filing team bounds what it gets (e8041e94c, "Capabilities and repository
  access" above): an unauthorized one is stripped when the task is filed,
  with a note on its thread. A schedule's `capabilities:` refuses
  `repo-read:` and `repo-write:`, which belong on the template it fires.

Each of these is refused with its own reason rather than quietly dropped,
so somebody who writes one finds out from the file rather than from a job
that never does what they meant. `network:` is the one isolation setting
a file may carry, because it can only take something away: `none` is a
repository choosing to run its own standing job with no route off its
sandbox's segment.

`model:` is carried by templates and schedules too, because it changes
what runs the work and nothing the work may do. It names a model, never a
connection: the run goes to whichever of the team's own connections
advertises that model (docs/connections-and-models.md), so a file cannot
reach a provider account its team does not already have. A model nothing
in the team serves leaves the task waiting in the queue, saying so. A
schedule's `model:` beats the model of the template it fires. A schedule
that runs a suite refuses it, because a suite run files each template as
it stands, so the model belongs on those templates.

`interactive:` is carried by templates for the same reason: it decides
who the agent talks to while it works -- whoever is watching the task,
through the `chat` tool -- and nothing the work may touch
(docs/schedules.md, "Interactive templates").

Two smaller sets of keys are refused for their own reasons, and say so
too. A schedule's `title:`, `body:`, `auto-merge:`, `network:` and
`custom-fields:` belong to the template it fires, not to the cadence. And
`id:`, `created-at:`, `next-run-at:` and `last-run-at:` are not the
file's to write at all: an id is derived from the repository and the
file's own name, and the firing times are grain's record of a cadence
already counting down.

All of them -- what a declaration may write, what it may not, and what
each refusal says -- are one list, `pkg/declschema`, shared with the
settings these files sit beside so that a field cannot land in one and
not the other. docs/declaration-schema.md is that argument, and the
decision about identity that goes with it.

The same reasoning is `model.RepoWorkflow`'s -- "being operator config
buys this row the right to say *what runs*; it must never grow the right
to say what a job may touch" -- read from the other side.

## Turning it on

Off by default, per repository: `RepoConfig.Declarations`, which is
`PUT /api/repos/{owner}/{name}/declarations` or a `repo_config` row in the
state repository. Nothing is read from a repository until an operator
turns it on there.

An unattended schedule is a standing claim on this deployment's agent
capacity, so somebody with access to grain's settings decides once, per
repository, that this repository's own word is good enough for that. What
a declaration may then say is bounded separately, and much more tightly,
by the rules above: the answer to "what may this task touch" is never the
repository's, switch or no switch.

Turning the switch off removes everything that repository declared, on
the cycle that notices and before anything can fire one of them again.

## How it is kept up to date

A poll, in the reconcile cycle (`orchestrator.reconcileDeclarations`,
running second so a schedule that arrives this tick can come due on it).
There is no event to subscribe to that a restart or an outage would not
miss, and a reconciler that re-reads the world heals itself -- grain's
own "input is a model update, not a GitHub issue", applied to a file in
somebody else's repository.

Every couple of minutes, each switched-on repository's default branch tip
is checked with one `git ls-remote`. A tip that has not moved costs
nothing more. A tip that has moved is cloned shallow on the daemon's own
host -- with the credential the deployment already holds, into a
directory it deletes -- parsed, and written to the store in one
transaction.

What that transaction does:

- Declared rows live in the ordinary `template`, `suite` and `schedule`
  tables, so everything that fires, composes or renders one works
  unchanged. They are told apart by their id, which carries the `repo:`
  prefix (alongside the existing `system:`) and is derived from the
  repository and the file rather than allocated from a sequence.
- `repo_declaration` records which repository and file each id came from.
  That is what makes re-reading the same tree idempotent, what identifies
  a row whose file has been deleted so it can be removed, and what lets
  the UI refuse an edit by naming the file to change instead.
- A schedule's next and last firing times survive a re-read. They are
  grain's record of a cadence already counting down, not something the
  file says; retiming them on every poll would leave a daily job
  permanently a day away.
- A repository that cannot be read -- unreachable, or with a file that
  does not parse -- keeps the declarations it already had, and the reason
  is recorded against it (`repo_declaration_sync.last_error`, shown on the
  repository's own settings). Silently switching off a deployment's
  standing work over a syntax error would be much the worse failure.

## Editing one

You cannot, from grain: a declared template, suite or schedule answers a
`PATCH` or a `DELETE` with the repository and file to change instead
(`ui.Client.refuseDeclaredEdit`). The next poll would put any edit back
within a couple of minutes, with no record of what happened to it.

This is deliberately not the split `refuseSystemScheduleContentEdit`
makes for one of grain's own jobs, where the content ships in grain's
code and the deployment owns the cadence. A declared row has one author
for the whole of it. What the deployment keeps is the switch.

## Where the code is

- `pkg/declschema` -- the one definition of what a template, a suite and
  a schedule are made of, shared with grain's own settings and the
  settings export (docs/declaration-schema.md).
- `pkg/repodecl` -- the format: `Load` reads a tree into
  `model.RepoDeclarations`.
- `pkg/model/repo_declaration.go` -- ids, provenance, the vocabulary, and
  the reasoning above in full.
- `pkg/model/repo_declaration_store.go` -- `SyncRepoDeclarations` and
  what it preserves.
- `pkg/orchestrator/declarations.go` -- the poll.
- `pkg/model/path_trigger.go`, `pkg/repodecl/triggers.go`,
  `pkg/orchestrator/pathtrigger.go` and `pathtriggerhold.go` -- path
  triggers: matching, parsing, firing on merges, and the pull-request gate.
- `pkg/model/sensitive_path.go`, `pkg/orchestrator/repoguard.go` and
  `pkg/ui/approvals.go` -- sensitive paths: named approvers, the guard
  over a repository's own triggers, and Submit's check.
- `cmd/grain/declarations.go` -- the clone that reads a real repository.
- `cmd/grain/declcmd.go` -- `grain declarations check`, the same read of a
  working tree, with no daemon behind it.
- `pkg/ui/declarations.go` -- the refusal and the provenance on the wire.
