# `.grain`

What this repository declares to grain, as files in the repository
rather than as rows in a grain deployment's database.

`workflows/` is the checks and releases grain's own CI (`pkg/ci`) runs
against a branch of this repository -- one YAML file per workflow, the
same way `.github/workflows` holds one file per GitHub workflow, and in
a format that is GitHub Actions' wherever GitHub has a word for the
thing. `pkg/ci`'s package documentation is the reference for the native
format: what is the same, what is deliberately different (`uses:` is not
implemented and `runs-on` names a sandbox rather than a runner pool), and
why.

`templates/`, `suites/` and `schedules/` are the standing work that
belongs to this repository: the templates its tasks are filed from, the
suites that run them, and the schedules that fire either -- again one
YAML file per thing. `docs/declarations.md` is the reference, and
`pkg/model/repo_declaration.go` is the argument for why a file here may
say what work to do in this repository and when, and may never say what
that work is allowed to touch. `settings.yml` is the same line drawn
across this repository's own settings row: two of them are a file here's
to write, and the rest are refused by name. `grain declarations check` reads a
working tree and refuses a file that does not parse.

`.grain` rather than `.github` because a repository should be able to
carry both without either reading the other's files, and because what
grain eventually needs to be told about a repository is not only its
checks.

The directory is also an explicit override. If a repository has no
`.grain/workflows` directory, Grain reads its existing
`.github/workflows` files and executes the supported GitHub Actions
surface through `act` inside the same sandbox. Creating this directory,
even empty, opts out of that fallback.

## What a file here is trusted with

Nothing by default, and that is load-bearing rather than incidental.

Every agent grain dispatches into this repository can edit these files.
So a workflow must never be able to widen what anything is allowed to
do: a job's sandbox gets no credential or capability material except the
capabilities configured for that repository, and *whether* grain CI runs
for a repository at all is a decision made outside the repository -- the
`grain-ci` capability, which an operator attaches per task, per repo or
deployment-wide (`pkg/capability/grainci`). What a workflow can spend is
sandbox time and outbound network access, in a sandbox where the agent
that could have edited the file was already running whatever commands it
liked. Open egress matches a GitHub-hosted runner; the security boundary
is that a workflow cannot grant itself anything.

"Configured for that repository" is one place, and it is not this
directory. An operator sets the set in Repos -> CI capabilities or with
`grain repo ci-capabilities`, and nothing in `.grain` can add to it or
subtract from it: `.grain/settings.yml` accepts `prompt-extension` and
`setup-command`, and every other key of a repository's settings row is a
parse error naming the key and the reason (`pkg/declschema`'s
`RepoSettingsSchema`, grain/task-340).

A job here *may* name a capability, and that is not a hole in the
paragraph above (grain/task-486). `capabilities:` and `secrets:` on a
job say what its steps cannot run without -- a requirement, not a grant.
Writing `package-publish` into a workflow file produces no credential:
it makes that job refuse to run, in those words, on any deployment whose
row for this repository does not already offer it. What it buys is that
the failure names the missing grant instead of surfacing minutes later
inside an unrelated step, and that the other six jobs of the suite still
run.

Three jobs here declare, and no others (grain/task-541):
`release.yml:images` and `image.yml:guest-cache` name `package-publish`,
because each is an hour or more of building whose only point is the push
at the end of it, and `live-agent.yml:live-agy` names `gemini-key`,
because every step of it drives a real agy CLI against a real provider.
Nothing in `workflows/tests.yml` names anything: that file began as
the removed `.github/workflows/tests.yml`'s credential-free half and
none of its jobs opens a credential. That silence is the point rather than an
oversight -- a job that declares no capabilities keeps being handed the
whole provided set, so the key is only worth what declaring it where the
requirement is real buys. (Release secrets do not work that way: a job
that names no `secrets:` is given none -- `docs/declarations.md`.)

Until 21 Sep 2026 it was two places: a `ci-capabilities` list here
declared the set too. The argument for that was that declarations are
synced from the repository's **default branch** head
(`orchestrator.syncDeclarations`), so a branch cannot widen its own run
and a new grant is a reviewed merge. That argument is true and it is not
enough, for two reasons. The first is what it rests on: a reviewed merge
in a repository grain works in is frequently reviewed by grain, on a
task that merges itself, so "an operator decided" and "it was reviewed
here" are not the same sentence -- and the capability model is the one
thing that cannot be allowed to depend on which of them held. The second
is that it had already gone wrong. At 18:52 UTC on 20 Sep 2026 an
operator switched declarations on for this repository, the settings file
was applied against a row nobody had read into it, and a reviewed merge
took `gemini-key` off the CI set and blanked the job that fills the
sandbox guest cache; `main` moved about fifteen times before anybody
noticed (grain/task-460, 483, 464).

Then it went wrong a second time, on the way out. Withdrawing the file
used to blank the row as thoroughly as applying it did: the old
`deleteDeclared` reset a withdrawn settings declaration's `repo_config`
to `RepoConfig{Repo, Declarations}`, so *deleting* `.grain/settings.yml`
cleared every field on it, including the ones the file had never been
allowed to write. That is what the merge narrowing this file to two keys
did on its way in. At 04:42:44 UTC on 21 Sep 2026 grain/task-340's own
merge withdrew the file, the daemon then running synced `main`'s head,
found the declaration gone, and zeroed the row -- `default-capabilities`,
`ci-capabilities`, `watch-default-branch`, `release-workflows-disabled`
and the `post-merge-dispatch-jobs` entry all at once. The guest cache
stopped following `main` from the very next reconcile cycle
(`orchestrator.startDueBaseBranchDispatch` skips a repository naming no
jobs, silently), and no release started either, because a repository
with no `grain-ci` in its defaults is one `grainCIEnabledForRepo`
answers false for. `main` moved about twenty times over the next twelve
hours before anybody asked why (grain/task-514).

`deleteDeclared` now takes back the two fields the file may write and
leaves the rest of the row as an operator wrote it, so the trap is
closed in this tree. It is worth knowing that closing it in this tree is
not the same as closing it in the deployment: that daemon runs a
published image, this repository's releases are the thing that publishes
it, and the row the incident blanked is the row that says whether
anything publishes at all. A deployment can therefore be left running
the build that has the bug, unable to reach the build that fixes it, by
the bug. Restoring the row is the way out, and it is an operator's edit
to `grain/config`, not anything a branch here can do.

So the values that file used to carry live in this repository's
`repo_config` row, and only there. If this repository's CI, publishing
or guest cache is ever mysteriously reconfigured, that row is what to
read -- through grain's Repos pane, or `tables/repo_config.json` in
`grain/config` -- and it should hold `grain-ci` as a task capability,
`package-publish` and `gemini-key` as CI capabilities, `ignore-github-ci`
and `watch-default-branch` set, `work_overrides` holding `release=off`
(this is what `release-workflows-disabled` became in grain/task-486 --
the release is one work item this deployment does not run, rather than a
switch of its own), `.grain/workflows/image.yml:guest-cache` as its
post-merge dispatch job, and an empty base-branch job list -- which
since grain/task-458 and 461 means the whole push plan rather than no
plan. Nothing in this repository can restore any of that; it is an
operator's row.

### Reading that row when you cannot reach it

Checking that it holds them is not something most work here can do. A
task sandbox scoped to this repository can read neither the row nor
`grain/config`; both answer `403 ... is not in scope for this sandbox`,
which wants either `self-debug` or a grant nothing in this repository
confers. That is inconvenient exactly when it matters, because the
window in which somebody wants to know is the window in which the row
is wrong.

It can be read off what grain is observably doing instead. Every value
above has a symptom in `list_ci_runs`, and between them they cover the
row:

- `watch-default-branch`: there is a grain CI run for `main`'s current
  tip. Unset, `main` is judged by nothing, and the newest run on it is
  the one from before the setting went.
- the base-branch job list: that run carries every job
  `workflows/tests.yml` runs on a push. A shorter one is somebody's
  cost narrowing rather than the default.
- `work_overrides` holding `release=off`: no `release images` runs for
  `main` while `main` is moving.
- `gemini-key`: `workflows/live-agent.yml`'s scheduled `live agy run`
  prints the agent's environment, and `GEMINI_API_KEY=[redacted:
  GEMINI_API_KEY]` in it is the grant. Without it the job says so at
  its `Require a credential` step instead.
- `grain-ci` in the task defaults: a task filed with nothing granted to
  it by hand can start CI at all.
- the post-merge dispatch job: `sandbox guest cache` runs for `main`, a
  few merges apart. One dispatch is in flight at a time and the next
  starts against the tip as it then stands, so it follows `main` rather
  than appearing once per merge -- but a stretch of merges with none is
  it not being dispatched.

That last one is the one to check first, because it is the one nothing
reports: the content-addressed cache stops following `main`, and every
CI job that resolves a sandbox guest goes back to building a ~1GB one
inline, an hour later, in the middle of somebody else's suite. It also
has two sources rather than one.
`pkg/model.EffectivePostMergeDispatchJobs` adds the guest cache job to
whatever the row names when releases are off, `package-publish` is on
the CI set and this deployment runs a guest this repository publishes --
so no `sandbox guest cache` runs at all means both that the row does not
name the job and that one of those three does not hold.

`package-publish` is the one value with no symptom of its own, which is
worth knowing rather than hunting for: grain places its material only
for a release or a dispatch and never for a push, so no job a branch can
start will report whether it is granted. It shows up indirectly, as the
guest cache not following `main` even where the row names the job.

Read the other way round, this list is what the incident above looked
like from a branch while it was happening. grain/task-505 read it that
way at about 16:00 UTC on 21 Sep, while the row was partly restored:
`grain-ci` in the defaults, `watch-default-branch`, the whole push plan
over `main`, `gemini-key` and `release=off` all held, and the dispatch
job did not -- `sandbox guest cache` had run on `main` six times between
23:53 UTC on 20 Sep and 03:55 UTC on 21 Sep, and not once in the twelve
hours after 04:42, which is the withdrawal above to the minute. It was
dispatching again by 22:00 UTC the same day.

The publishing capabilities -- `package-publish`, which places this
deployment's own GitHub credential for the repository, and `registry-push`,
which places a registry secret an operator pasted -- are narrower again. Grain
materializes either only for the trusted `release` event it creates after
observing a merge, never for a caller-created run, pull request, or ordinary
push. A release workflow can therefore publish without making a credential
that writes this repository's packages available to code before it merges.

A repository may also carry custom release secrets in Grain's encrypted secret
store. The repository page records only their names; each value is materialized
as a mode-`0600` file below `/home/debian/.grain-release-secrets` for trusted
`release` and explicit `workflow_dispatch` runs. Ordinary push and pull-request
jobs receive neither the names nor the values. The same repository page can
suppress the automatic post-merge event without deleting its workflow or
credentials, and can explicitly run the release event against a chosen branch.

That first half is a claim a test holds rather than a comment:
`cmd/grain/ci_hostile_test.go` commits a workflow written to ransack the
machine it is given -- every path a capability places material at, every
credential store, a push of the repository back, a fetch of a repository
it was not given, the host behind the git proxy -- runs it through the
real adapter into a real sandbox, and asserts on what its steps found.

## Repositories grain does not control

They will not have this directory, and the plan is not to ask them for
one: the same workflows will be declarable on grain's own side, against
the repo, and read through `ci.Source` -- which is why loading them is
an interface with the in-repo directory as one implementation rather
than the only way there is to find a workflow.
