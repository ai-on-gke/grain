# Repository CI capabilities

Open **Repos → your repository → CI capabilities**, choose capabilities and
click **Save CI capabilities**. They apply to every job in the next CI run
that does not narrow them (see [What a job is given](#what-a-job-is-given)),
including runs requested manually or through an agent. No daemon restart is
needed. Clearing the list returns CI to a read-only checkout without capability
material. Agent task defaults do not grant anything to CI.

Repository access (`repo-read:` / `repo-write:`) is not granted here. A
job declares it in its own workflow file, and its team's GitHub
credential decides whether it holds. See
[Repository access](#repository-access-declared-in-the-repository-enforced-by-the-teams-credential).

The equivalent CLI is:

```sh
grain repo ci-capabilities -set gcp-key,github-sandbox bwsalmon/grain
grain repo ci-capabilities bwsalmon/grain
grain repo ci-capabilities -set '' bwsalmon/grain
```

The API is `GET` / `PUT /api/repos/{owner}/{name}/ci-capabilities`, with a PUT
body such as `{"ciCapabilities":["gcp-key","github-sandbox"]}`. The database
stores the list in `repo_config.ci_capabilities`; existing databases migrate to
an empty list. State/settings exports preserve it with the other repo settings.

CI supports capabilities that mint leased sandbox material (GCP keys, Gemini
keys, temporary GitHub repositories, and Claude/OpenAI/Gemini proxies) and the
release-only publishing credentials described below.
Agent-only MCP tools and named Git credential selectors are not offered.
Configure the providers in Settings first. Missing or refused providers fail
job preparation and report the capability's error before workflow steps run.

A job that needs GCP access in a project other than the deployment
pool's is granted one of the deployment's named GCP keys,
`gcp-key:<name>`, here instead. See [GCP keys](team-gcp-keys.md) for the
setup.

The publishing credentials go the other way as well: `package-publish` and
`registry-push` are **CI-only** (`model.CapabilitySpec.CIOnly`), so this
repository setting is the only place either is granted. The task capability
picker does not offer them, they cannot be put in a deployment-wide or
per-repo task default set, and a task that holds one anyway -- seeded before
this rule existed, or written straight into the store -- is refused at
`model.ResolveGrants` before its provider resolves anything, so no credential
is minted and nothing is placed. The reason is the event gate below: the
material is a token that can write to the repository's own packages, and it
is released only to a trusted post-merge release or an operator's own
`workflow_dispatch`. A task is not an event, and "this task is working on the
default branch" is not the assurance "this commit is already merged" is, so
there is no task-path equivalent of that gate to apply. Settings shows both
capabilities as **CI only** rather than as something a task could be given.

## What a job is given

A job's `capabilities:` key decides how much of the repository's CI
capabilities its sandbox receives (`jobCapabilities` in
`pkg/ci/requirements.go`). There are three cases, and the first two are not
the same:

| In the workflow file | What the job is given |
| --- | --- |
| no `capabilities:` key | **everything** this list grants the repository -- what every job got before the key existed |
| `capabilities: []` | **nothing** from this list (only what it names under `optional-capabilities:` and is granted) |
| `capabilities: [gcp-key]` | exactly what it names; a name this list does not grant means the job does not run, and says which |

A job given a capability mints its material at admission -- a GCP
service-account key, a Gemini key -- and queues for it where the provider's
pool is finite, whether or not its steps ever read it. So a job that uses no
credential should say `capabilities: []`: on 2026-10-23 every job of
`.grain/workflows/tests.yml` omitted the key and minted a `gcp-key` and a
`gemini-key` per job, which exhausted the GCP key pool and IAM's write quota.
This repository's own workflows now declare `capabilities: []` on every job
that uses none, and `gcp-smoke.yml`'s `smoke` job is the one that asks for
a GCP key -- as the gcp server's reference
`https://grain.dev/capabilities/gcp#gcp-key`, not the legacy `gcp-key`.

Repository access (`repo-read:` / `repo-write:`) and capability references
are unaffected by all three: they are granted by being declared, never by
this list ([below](#repository-access-declared-in-the-repository-enforced-by-the-teams-credential)).
A grain build older than the empty-list rule reads `capabilities: []` as an
absent key and hands the job the whole set, as before.

## Capability servers

The providers on this page are the in-process ones in `pkg/capability/`, and
CI keeps using them until runs are moved over to capability servers
([docs/capabilities-spec.md](capabilities-spec.md)). Their capability-server
equivalents are already in the binary, served by `grain capability <name>`:
`gcp` (`#gcp-key`, for `gcp-key` and `gcp-key:<name>`), `model-proxy`
(`#model-proxy`, for the `<vendor>-proxy` capabilities), `git`
(`#git-repo`, for repository access), `self-debug` and `skill`. In a
`.grain/workflows` job they will be granted as references such as
`https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key` with `with:`
arguments; the arguments, the environment each server reads its credential
from, and what each mint places are in
[docs/capabilities.md](capabilities.md#in-tree-capability-servers-grain-capability-name).
A job naming a reference already has it minted before its sandbox is
built, through the server attached for it in `mcpServers`: the reference
need not be in the repository's CI capabilities, the deployment's
`capability_policy` decides instead (as run kind `ci`, with the run's
event), a refusal concludes the job `errored` quoting the server's reason,
and the grants are released when the job ends. What a mint returns is
carried into the job's sandbox after its checkout and before its first
step: ports forwarded, placements written, setup commands run (a failing
one concludes the job `errored`, quoting its output as the server's), and
each `mcp_endpoint` (`self-debug`) proxied, forwarded into the sandbox over
the capability port relay and listed in `/home/debian/.grain/mcp.json`
(spec §5.2.4) as `{"mcpServers": {"<name>": {"type": "http", "url":
"http://127.0.0.1:<port>/<secret>/mcp"}}}`. Ports and endpoints both reach
the job over the sandbox fabric, so they stay reachable under
`network_access: "none"` (spec §5.3.3). The job's automatic retry gets the
same grants in its fresh sandbox, its endpoint proxies re-pointed there,
without minting again.

## Which trees are trusted with them

These capabilities are granted to the trees on the repository's own
branches, the line GitHub draws between `pull_request` and
`pull_request_target`. Grain never starts CI for a pull request it did not
open itself: there is no webhook or pull-request poller behind grain CI, and
the `pull_request` runs it does start are merge-queue batches built from
grain's own task branches. The one way a run could still reach somebody
else's tree is a pinned commit. A job checks out the commit a run names, and
GitHub serves a fork's pull request head by commit (`refs/pull/N/head`). So
when a run's `commit` is neither the branch's tip nor in its history,
`ci.Service.Start` withholds **every** capability and release secret from it.
A job that declared one errors before it starts, saying the commit is not on
the branch. A job that declared nothing, which would otherwise be handed
everything this list grants, gets nothing.

Release secrets go to a `workflow_dispatch` or `release` run. When somebody
starts one of those by hand through `POST /api/ci/runs`, grain's equivalent
of GitHub's "only somebody with write access" rule applies: the caller must
be a person rather than an agent, the same rule as
`POST /api/repos/{owner}/{name}/release-runs`. Anybody else's run still runs,
but without release secrets, and a job that declares one says so.

## Publishing packages

`package-publish` is how a release job publishes this repository's container
images. It needs no secret of its own: the login it places is **this
deployment's own GitHub credential for the repository under test** -- the one
the git proxy's ladder selects for that owner and repo, re-minted from an
App's private key where an App is what backs it
(`gitproxy.CredentialSet.PackageCredential`). Enable it and nothing else:

```sh
grain repo ci-capabilities -set package-publish bwsalmon/grain
```

Grain places the login at `/home/debian/.package-publish-{registry,username,token}`
-- `ghcr.io`, `x-access-token`, and the credential's current token -- only for a
trusted post-merge release or an operator-triggered `workflow_dispatch` run.
Pull-request and ordinary push runs cannot obtain it, and an agent's
task-scoped CI request remains an ordinary push. The repository whose
credential is resolved is the job's own, never one a workflow file names.

One `workflow_dispatch` run is started by Grain rather than typed by
anybody, and it is worth naming here because it carries this credential:
the jobs a repository lists in **post-merge dispatch jobs**
(`model.RepoConfig.PostMergeDispatchJobs`) are dispatched on its base
branch whenever that branch moves. What makes that the same act rather
than a loophole is where the list lives -- operator-owned repository
configuration, the same place this capability itself is granted from, and
unreachable from a task, a branch or a workflow file. The commit it runs
against is one already on the base branch, which is the other half of
what the gate asserts. It is narrow by construction too: those named jobs
and their dependencies, never the whole plan. `docs/merge-queue-behind.md`
has what it is for.

Since grain/task-410 that same list is dispatched in one more place, and
this one runs against a commit that is **not** on the base branch, so it
is worth reading rather than inferring. When grain plans a job that
resolves a sandbox guest whose content-addressed cache key nothing has
published -- which is what a branch that changes a guest input has -- it
dispatches those same jobs on *that branch's* commit, once, and every job
that needs the same guest waits for that one run instead of building a
~1GB image apiece (`pkg/grit/cache/guestcache.go`). The list is still
operator-owned and the run is still narrowed to it, but the tree it
executes is the branch's: the job that publishes is the branch's copy of
it, and what it publishes under the content key is what `main` pulls once
the branch lands. That is the same trust `.grain/workflows/image.yml`
describes an operator placing when they dispatch that job by hand for a
branch, and it is now placed by grain on their standing instruction
rather than per branch. A repository that names no post-merge dispatch
jobs has none of this: nothing is warmed and every job resolves its own
guest, which is what they all did before.

Since grain/task-457 one job reaches that list without an operator
putting it there, and it is the only thing in this document Grain
decides for itself. The repository publishing the sandbox guest *this
deployment runs* -- read off the image its own sandboxes are created
from, so it is one repository and Grain cannot be talked into thinking
it is another -- has `.grain/workflows/image.yml:guest-cache` dispatched
on its base branch while its release workflows are switched off. The
reason it is not a loophole is that it grants nothing: the run is a
`workflow_dispatch` against a commit already on the base branch, which
is what this gate asserts and all it asserts, and Grain adds it only for
a repository whose CI an operator has *already* granted
`package-publish`. Withhold that grant and nothing is dispatched --
which is the same answer the gate gives everywhere else in this
document, arrived at before the job starts rather than at its first
step. `docs/merge-queue-behind.md` has why the default exists.

Two things follow from publishing as the deployment's credential rather than
as a token of its own, and both are the point of it: the credential that
publishes is the credential that pushes, so a rotation covers both at once and
an App-backed deployment publishes with a token that expires within the hour;
and the credential has to carry write access to the repository's packages
(`packages: write` on a GitHub App installation, or `write:packages` on a
classic PAT -- a fine-grained PAT cannot grant it). Because `docker login
ghcr.io` accepts any valid GitHub token without checking its package scopes,
`PackageCredential` refuses a fine-grained PAT (`github_pat_...`) before the job
starts, `scripts/release-images.sh` checks a classic PAT's (`ghp_...`)
`X-OAuth-Scopes` for `write:packages` before building any images, and any other
credential without package write access is refused by the registry on the first
write (`permission_denied: The token provided does not match expected scopes.`)
without retrying. A repository this deployment has no credential for is refused
before the job starts, naming the repository.

A release job that was granted neither this nor `registry-push` has no login
at all, and `scripts/release-images.sh` stops on its first step rather than
building for an hour and being refused by the registry at the push. The job's
log names the capability and prints the command above. That is the whole of
the fix when a release ends in ghcr.io's `unauthenticated: User cannot be
authenticated with the token provided`: the grant is repository configuration,
and no change to a workflow file or to the script can supply it.

`registry-push` is the older shape of the same grant, for a registry that is
not GitHub's or a credential that should not be the deployment's. Store JSON with
`registry`, `username`, and `token` fields as
`registry-push/credential`, then add the capability to the repository's CI
capabilities. Grain places those values at
`/home/debian/.registry-push-{registry,username,token}` only for a trusted
post-merge release or an operator-triggered `workflow_dispatch` run.
Pull-request and ordinary push runs cannot obtain it, and an agent's
task-scoped CI request remains an ordinary push.

## Signing images

`image-signing` places the cosign key a release signs this repository's
images with (`docs/image-signing.md`): the encrypted private key at
`/home/debian/.image-signing-key` and its password at
`/home/debian/.image-signing-password`, which are the paths
`scripts/release-images.sh` reads. It is CI-only and admin-team only, like
`package-publish`, and it is deliberately narrower than that grant: it is
honoured **only for the `release` event**, never for `workflow_dispatch`.
`.grain/workflows/image.yml`'s `guest-cache` runs under `workflow_dispatch`
from branches, so a key released to dispatches would be a key any branch's
steps could sign with -- and a signature made by whoever can push to the
package proves nothing beyond push access.

The key comes from grain's secret store, not from any credential the
deployment already holds. To set it up:

```sh
cosign generate-key-pair             # writes cosign.key and cosign.pub
grain secrets set -value-file cosign.key image-signing key
grain secrets set image-signing password  # reads the password from stdin
grain repo ci-capabilities -set package-publish,image-signing bwsalmon/grain
```

and publish `cosign.pub` where deployments can fetch it (for example as
`docs/cosign.pub`). A job that is handed `image-signing` when either secret is
missing or empty, or when `image-signing/key` is not a PEM private key, is
refused before it starts, naming the secret -- so set the secrets before the
grant, since an optional declaration (below) is placed as soon as it is
granted.

`.grain/workflows/release.yml`'s `images` job declares it as
`optional-capabilities: [image-signing]`. It was a requirement once
(d0d4ff46b), and a deployment whose build predated the provider refused every
release for it -- including the one that would have published a build with the
provider (grain/task-2016). As an optional capability, a granted key signs
every release while an ungranted one leaves it unsigned (with a warning from
`scripts/release-images.sh`) rather than unpublished.

## Optional capabilities

`capabilities:` on a job is a requirement: a job naming one this deployment
does not provide for the repository does not run, and says which
(`pkg/ci/requirements.go`). A job that *uses* a capability when it is there and
can do its work without it declares it under `optional-capabilities:` instead:

```yaml
jobs:
  images:
    capabilities: [package-publish]          # refused without it
    optional-capabilities: [image-signing]   # placed when granted
```

- It is placed exactly when a required one would be: the deployment provides
  it and the repository's CI capabilities grant it.
- When it is not -- not granted, granted only to another repository, withheld
  from an untrusted commit, or a name this build has never heard of -- nothing
  is placed and the job runs. It is never a reason to refuse the job.
- It narrows like a requirement: a job still gets **only** the capabilities it
  declares, required or optional. A job that declares only optional ones gets
  those it is granted and nothing else, not the repository's whole set.
- A job that declares nothing still gets the repository's whole set, **except**
  the scarce capabilities -- those whose provider mints a finite external
  credential at admission (`model.ScarceResourceProvider`: `gcp-key`,
  `gcp-key:<name>`, `gemini-key`). A job gets one of those only by naming it
  under `capabilities:` or `optional-capabilities:`. Handing them to every
  undeclared job minted and deleted a service-account key per job of every
  run, which exhausted IAM's per-minute write quota (grain/task-281).
- A name cannot be in both lists, and `repo-read:`/`repo-write:` cannot be
  optional: declaring repository access is what grants it, so it belongs under
  `capabilities:`.
- The key is new: a grain build older than it refuses a workflow file that
  uses it, as it does any key it does not know, and that stops every job in the
  repository. A workflow that must run on such a deployment cannot use it yet.

Publishing jobs should disable caching: a successful result is evidence that a
publish happened once, not permission to skip publishing on the next run.

Grain's own publisher (`.grain/workflows/release.yml`) answers `release` and
nothing else, so `grain ci run` cannot ask for it: a release event is not one a
caller may request. Running it again -- after a flake in the container suite it
now publishes behind, or to republish a commit -- is the deployment's own
release run: `POST /api/repos/{owner}/{name}/release-runs` with a body naming
the branch (`{"branch":"main"}`), which is the one caller Grain trusts to say
`release` besides its own merge reconciler. Because that run is handed the
release secrets, the endpoint answers only a person -- whoever holds the access
code, the deployment's owner -- and never an agent. Anything but a person would
also be held to a watched base branch or a branch of an open release, and could
not pin a `commit`: the workflow a run executes is read at its ref, and a task
branch's workflow is whatever its author wrote.

The gate is the run's event, not the job: **every job of a trusted release run
is handed these credentials**, whether or not it publishes anything. So
`release` is a trigger to add to a job that has to publish and to nothing else
-- a test job that answers it is handed a credential it has no use for, in a
run whose conclusion nobody is waiting on.

Nothing needs to answer `release` in order to judge a base branch, and this is
the paragraph that used to say otherwise. Grain asks for a base-branch run
under `push`, for every repository it judges, whatever its releases are doing
(`orchestrator.baseBranchCIEvent`, a constant since grain/task-461; which
repositories it judges is the `base-ci` standing work item, grain/task-3332): the
judging run is untrusted, is handed none of these credentials, and publishes
nothing, while the release a merge emits is a second run at the same tip that
the merge queue does not read. A repository whose tests answer `push` therefore
has them in its gate already, and `RepoConfig.BaseBranchCIJobs` is what narrows
that gate where a repository wants it narrower -- including a repository whose
publish job sits in its own push plan, which is how that job is kept out of the
gate. The rule this replaced -- judging jobs answering `release` beside `push`,
which `.grain/workflows/tests.yml` here did for a while -- put the whole suite
inside the trusted run and left a flaky publisher holding every merge in the
repository. `docs/merge-queue-behind.md`, "Releases on: the judging jobs answer
`release`", keeps it and the alternatives weighed on the way out of it, because
a deployment that followed it has it written into a workflow file.

A branch that wants the image built and driven *without* publishing anything
asks `.grain/workflows/image.yml` instead, which is `workflow_dispatch` only:

```sh
grain ci run -repo bwsalmon/grain -branch <branch> \
  -job .grain/workflows/image.yml:container-e2e -watch
```

For example:

```sh
printf '%s' '{"registry":"ghcr.io","username":"USER","token":"TOKEN"}' |
  grain secrets set registry-push credential
grain repo ci-capabilities -set registry-push bwsalmon/grain
```

Each job gets separate material at the same paths used by agent runs. For
example, a workflow using the OpenAI proxy can run:

```yaml
steps:
  - run: |
      export OPENAI_BASE_URL="$(cat /home/debian/.openai-proxy-url)"
      export OPENAI_API_KEY="$(cat /home/debian/.openai-proxy-token)"
      ./run-tests
```

Other paths are `/home/debian/.gcp-service-account.json`,
`/home/debian/.gemini-api-key`, `/home/debian/.github-sandbox-token`, and
`/home/debian/.github-sandbox-repo`. On the host backend these absolute guest
paths are placed relative to the sandbox root, as for agent runs.

`/home/debian/.gcp-service-account.json` is the deployment `gcp-key`'s key
only. A named key, `gcp-key:<name>`, lands at
`/home/debian/.gcp-service-account-<name>.json` (`gcpkey.KeyPath`), so a job
granted `gcp-key` and `gcp-key:deploy`, or two named keys, gets every key in a
file of its own rather than only whichever was placed last.

Workflow YAML, a caller's task capabilities and the repository's own files
cannot widen the set. The set is an operator's, as above, and only an
operator's: `.grain/settings.yml` carried a `ci-capabilities` list until
21 Sep 2026 and no longer does -- writing one there is a parse error naming
the key (`pkg/declschema`, grain/task-340). The argument that used to justify
it, that declarations are read from the repository's default-branch head so a
branch cannot widen its own run, is answered in `.grain/README.md` under "What
a file here is trusted with": it makes the capability model depend on review
discipline inside the repository, which is the one thing it cannot depend on.
Changes affect new runs;
already-started runs keep their snapshot, including queued jobs. The set is
part of the cache and live-run deduplication keys. Existing cached results
cannot be reused across capability changes.

Materialized leases are revoked on success, failure, cancellation, and partial
setup failure. Providers retain their existing expiry/reaping backstops for a
controller crash; cleanup errors are recorded in the daemon log.

## Grain's API: `api:<scope>`, `api:<preset>` and `release-gate-read`

A job can hold agent API grants (`api:ci:read`, `api:tasks:read`,
`api:release-gate-read`, ...). [docs/agent-api.md](agent-api.md#ci-jobs)
describes what they are worth in CI: one bearer token per job, good only
against grain's CI API listener, held to the routes of the scopes the job
holds and to the job's own repository.

`release-gate-read` gives a release workflow
(`.grain/workflows/release-train.yml`, docs/design/release-from-trunk.md
§6) the deployment state it needs to choose a commit and judge a soak.
It is the built-in preset `api:release-gate-read` (`ci:read`,
`tasks:read`, `sandboxes:read`, and `tasks:from-template` limited to
`release-notes`), and its bare name is an alias for it:

| Request | Answers with |
| --- | --- |
| `GET /api/ci/runs?ref=main&limit=100` | the CI runs of the job's own repository (`scripts/candidate-gate.sh --runs-json`) |
| `GET /api/persistent-sandboxes` (also `/api/system/persistent-sandboxes`) | the persistent sandboxes, `grain-candidate` among them |
| `GET /api/tasks?limit=100` | the tasks of the job's own repository |
| `POST /api/tasks/from-template` with `{"fields": {...}}` | a task filed from the repository's `.grain/templates/release-notes.yml`, and no other template |
| `POST /api/notifications/release-pending` with `{"version": "vX.Y.0", "rc": "vX.Y.0-rc.N", "body": "..."}` | the operator notification `release.pending:vX.Y.0` ("vX.Y.0 is ready to promote") about the job's own repository, with the Approve and Reject actions; subject, title, severity, repository and actions are fixed by the gate |

No request it allows approves a release: that is
`POST /api/repos/{owner}/{name}/pending-releases/{version}/approve`, which
only a person may call (docs/design/release-from-trunk.md §4).

The job finds the listener in `/home/debian/.grain-api/`:

```sh
. /home/debian/.grain-api/env   # GRAIN_API_URL, GRAIN_API_TOKEN
curl -fsS -H "Authorization: Bearer $GRAIN_API_TOKEN" \
  "$GRAIN_API_URL/api/ci/runs?ref=main&limit=100"
```

The address and the token are also in `url` and `token` in the same
directory. The token stops working when the job's sandbox is released.

Like repository access, a job gets api grants by declaring them
(`capabilities: [release-gate-read]`), and they are not granted from this
list. They are CI-only in this form. Like `repo-write`, they are released
only to a `release`, a `workflow_dispatch` and a cron grain fires itself.
They are honoured only for jobs of `bwsalmon/grain`, since the state they
read belongs to the whole deployment, and a dangerous scope is refused
outright.

## Repository access: declared in the repository, enforced by the team's credential

`repo-read:<owner>/<repo>` and `repo-write:<owner>/<repo>` are not
granted from this list. **The declaration in the repository is the
allowance; the team's GitHub credential is the authority.** A job gets
read or write access to a repository only when both of these hold:

1. **The job declares it, in the repository, on the commit being run:**

   ```yaml
   # .grain/workflows/nightly.yml
   jobs:
     publish-notes:
       capabilities: ["repo-write:acme/widgets"]
   ```

   The declaration is least-privilege. A job that declares no
   `repo-write:` never gets write, even if its team's token could push.
   A job that declares only `repo-read:` gets fetch access through the
   proxy and no push.

2. **The team's own GitHub credential can do it.** A job holding
   repository access it declared runs as a team. Its checkout, fetches
   and pushes all go through the git proxy with that team's credential:
   the team's override for the repository, otherwise the team's default
   (Team settings, Git identity). The deployment's own credential is
   never a fallback. GitHub refuses whatever that token cannot do, which
   is how a repository's owner decides which teams may reach it.

The team is the one that started the run. For a scheduled workflow, that
is the schedule's `team:` (docs/declarations.md), otherwise the team that
owns the repository, otherwise the default team. If that team has no
credential, the job fails before its checkout, naming the team and the
repository and pointing to Team settings, Git identity. If GitHub refuses
the team's token, the refusal git prints carries the same pointer. No
deployment administrator is involved at any step.

Neither part lowers the protections against untrusted code. `repo-write`
is still released only to a trusted post-merge `release`, a
`workflow_dispatch`, and the `schedule` run grain itself fires from a
workflow's `on: schedule` cron (`ci.Request.TrustedSchedule`, set only by
`orchestrator`'s scheduled-workflow starter and absent from JSON). A
`push` or `pull_request` run, and a caller's own
`grain ci run -event schedule`, still never hold write, whatever the job
declares. A provider opts in to trusted schedules with
`model.TrustedScheduleCapability`. `package-publish` and `registry-push`
have not opted in, so a cron still can't publish. A cron publishes
through `.grain/workflows/promote.yml` instead: once a `release-train`
schedule's run on the default branch finishes, the orchestrator starts
`promote.yml` as a trusted `release` of that commit
(`orchestrator.startPromotion`), and `package-publish` and `image-signing`
are released to that event.

### Which refs a CI job may push

Holding `repo-write:` does not let a job update every ref. The git proxy
checks each ref in the push (`gitproxy.CIRefUpdates`), and it applies
these rules however the job's YAML is written:

| Ref | Allowed |
| --- | --- |
| tags, such as `refs/tags/v2.4.0` and `refs/tags/v2.4.0-rc.1` | create, move (force) and delete |
| `refs/tags/latest` | create and force-move |
| other branches, such as `refs/heads/candidate` | create, force-move and delete |
| `refs/heads/main` | create, or a fast-forward the push shows. A force-push or a deletion is refused |

When one ref in a push is refused, the proxy refuses the whole push and
says why. It records every allowed update in the audit log as
`allowed: repo-write (<kind>)`.

Because a workflow file grants repository access this way, mark
`.grain/workflows/` sensitive (`human: required`) in `.grain/triggers`,
as docs/declarations.md recommends.

### Migration

Before task 1654, a job also needed an operator to put the same id in
this repository's CI capabilities
(`grain repo ci-capabilities -set …,repo-write:<owner>/<repo>`), and a
job that declared nothing was handed every `repo-*` entry in that list.
Both are gone:

- A `repo-read:` or `repo-write:` entry in `repo_config.ci_capabilities`
  is now a no-op. It no longer satisfies a job's declaration, and it no
  longer reaches a job that did not declare it. Existing rows are left
  as they are rather than rewritten, and can be removed at leisure.
- A job that relied on such an entry without declaring it must now
  declare it in its workflow file. Its team must also have a credential
  that can reach the repository.
- Everything else in this list, such as `gcp-key`, `gemini-key`,
  `package-publish`, `registry-push` and release secrets, is a
  deployment-owned credential that belongs to no team. Those are still
  the operator's to grant here.
