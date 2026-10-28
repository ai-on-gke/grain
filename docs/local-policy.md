# Local policy: grain as a Policy Enforcement Point

A Grain is single-tenant and manages one backlog. It may run on one
person's workstation (a desktop, a laptop), a VM or later a cluster.
Every task carries claims about it:

| Claim                  | Task field                      |
|------------------------|---------------------------------|
| `requester`            | `Task.Creator`                  |
| `approved_by`          | `Task.Approval` (a human actor) |
| `granted_capabilities` | `Task.Grants`                   |
| network permission     | `Task.NetworkAccess`            |

The Grain is the Policy Enforcement Point. It checks those claims
against a small policy that only the Grain's owner writes down, and runs
only what fits.

The claims are trusted, not verified. The Grain does not authenticate
the requester or independently check who approved a task, so anything
that can write a task's row could forge `requester` and `approved_by`.
Whatever a task claims, the policy guarantees that it gets no further
than its limits: the repositories, capabilities and network it allows.

Start the daemon with the policy file:

    grain daemon -data-dir ~/.grain -policy ~/.grain/grain_policy.yml

On a host installed with `scripts/setup.sh`, don't edit the unit: pass the file as
`GRAIN_POLICY` instead, and the installer writes `-policy` into
`grain-daemon.service`:

    sudo GRAIN_POLICY=/var/lib/grain/data/grain_policy.yml ./scripts/setup.sh

The daemon reads the file once, when it starts. An edit takes effect
only after a restart (`sudo systemctl restart grain-daemon`). Its log
names the file, the number of repositories it allows and its local
requesters:

    grain daemon: enforcing local policy /var/lib/grain/data/grain_policy.yml (1 repositories allowed; local requesters: debian) -- debian is this single-tenant daemon's owner, whom the UI and CLI file tasks as; edits to the file take effect when the daemon restarts

A task the policy holds stays queued, and the task pane shows why. The
daemon log says it every dispatch cycle too:
`dispatch: candidate 2 skipped: held by local policy: ...`.

Keep the file under the data directory, the only host directory a
container-mode daemon sees. A later re-run that doesn't set
`GRAIN_POLICY` keeps the installed unit's `-policy` and logs
`Local policy: keeping ... from the installed ...`, the same way it keeps
`-ui-addr`. `GRAIN_POLICY=none` is the only way a re-run
removes it.

Or name what the workstation works with directly:

    grain daemon -data-dir ~/.grain -policy ~/.grain/grain_policy.yml \
      -repo ~/src/widgets -repo acme/tools \
      -sandbox-backend container -model-backend claude

- `-repo` takes a local checkout's path (its `origin` remote is read) or
  `owner/name` or a URL, and can be repeated. Each repository is added
  to the policy's `repos`. It is also added
  to the default team's target repositories, but only when that list
  restricts them already. The first one becomes `-default-target-repo`
  unless that flag is set.
- `-sandbox-backend` is `container`, `granule`, `kubernetes`,
  `kubernetes-granule`, `host` or `none`. It is the same as passing
  `-container-sandboxes` and the rest. `host` is `-host-sandboxes`, with
  all of its checks: it needs `-sandbox-dir`, and it is refused on a
  daemon others can reach unless `-allow-unisolated-host-sandboxes` says
  so.
- `-model-backend` is `agy`, `claude` or `codex`, the same as
  `-agent-framework` (`agy` is antigravity).

Tasks filed from this workstation's own CLI can say who asked for them:

    grain create -title "..." -author bob@example.com

`-author` is recorded as the task's requester (`Task.Creator`). The
daemon still checks the filing against its own actor. Every caller it
lets in is that actor -- it presented the access code -- so `-author`
is accepted from any of them. A proxy's identity header or IAP assertion
names nobody here, so who asked for a task comes from `-author`, never
from the caller.

Without `-author`, a task filed from the CLI or the UI has the daemon's
own actor as its requester. That actor is `-as` if set, and otherwise
the OS account the daemon runs as (`debian` on a host installed with
`scripts/setup.sh`). The daemon therefore adds its actor to
`local_requesters` itself, so the owner's own tasks aren't held as
unapproved external requests: nobody else could ever approve them.

## The policy file

```yaml
# Repositories a task may target, write or read: owner/name or
# host/owner/name, case-insensitive. Either segment may be a glob.
# Empty (or missing) refuses every task that names a repository.
repos:
  - acme/widgets
  - acme-tools/*

# Requesters this machine treats as its owner. Their tasks, and grain's
# own (schedules and suites: no requester), need no further approval.
# Work derived from a task is judged as that task's root requester's
# (see "Derived tasks"). The daemon adds its own actor (-as, or the OS
# account it runs as) here itself.
local_requesters: [me@example.com]

# A task requested by anybody else must carry a human approval by
# somebody other than its requester. When approvers is set, only these
# people's approvals are accepted. allow_unapproved_external: true turns
# the approval check off.
approvers: [me@example.com, lead@example.com]

# Capability patterns, compared without regard to case. A bare family
# (gcp-key) matches it and every grant of it (gcp-key:team-a);
# family:* matches every parameterised grant (repo-write:acme/widgets);
# otherwise both halves are glob patterns (repo-write:acme/*).
capabilities:
  # When set, every capability a task may be granted.
  allow: [self-debug, gcp-key:team-*]
  # Refused whatever allow says.
  deny: [gcp-key:team-secret, repo-write:*]

# Network accesses a sandbox may have (open, none). Empty allows either.
# A task asking for none of its own gets the deployment default, and is
# checked as that.
network: [none]

# Only on -host-sandboxes, where every run executes as this machine's
# owner: let tasks that are not the owner's own run there too (below).
# Off by default.
allow_external_on_host_sandboxes: false
```

Unknown keys are an error. That way a misspelt restriction can't be
dropped silently.

### Repositories a grant reaches

`repos` judges every repository a task can reach, not only the ones it
names: a `repo-read:owner/name` or `repo-write:owner/name` grant answers
to `repos` exactly like the task's own target and reads, so
`repos: [acme/*]` refuses `repo-read:bwsalmon/grain`.

`self-debug` reaches no repository. A self-debug run's `grain-src/` is
the deployment's own baked-in copy of grain's source, placed in its
sandbox; grain no longer clones bwsalmon/grain for it, and the git proxy
does not let it fetch that repository. So a policy that does not list
bwsalmon/grain admits a self-debug task (`model.CapabilityRepos`).

## Derived tasks

A task filed from another one is judged as its root requester's, never
as grain's own just because it names nobody. That covers an agent's
`propose_task` proposal (filed live or at the end of a run), a port, a
stacked or pull-request-trigger review, a pull-request trigger's task
and a base-branch repair of the change that turned the branch red. Each
is filed with its source's requester and team
(`model.InheritRequester`).

At dispatch, grain walks the chain a task was derived from
(`model.Task.DerivedFrom`: its `proposed-by` link, its `port-of` link,
or a work origin naming another task) and judges it as the first
requester it meets. An empty requester counts as grain's own only when
the walk reaches a task derived from nothing. A chain naming a task
that cannot be read is judged as an external request.

Approval is never inherited. Ports, reviews, repairs and auto-submitted
proposals carry grain's own automation approval, which an external
request does not accept. So a task derived from somebody else's request
waits for a human to approve it. Release triage and merge triggers are filed for
a branch tip, not for one task's change, and stay grain's own.

## Host sandboxes

A daemon on `-host-sandboxes` runs every task as its owner, on the
owner's machine: the data directory (the database, secrets), `~/.ssh` and the owner's cloud logins are all within reach.
So by default it runs only the machine's own tasks: those whose root
requester (above) is in `local_requesters`, or is grain itself.
Anything else waits with the reason on its row, unless
`allow_external_on_host_sandboxes: true` lets it run. This rule applies
with no `-policy` file too: then a task whose chain cannot be read is
refused.

`-host-sandbox-env` (an SSH agent, a gcloud login, a proxy) goes only
to a run whose task is the machine's own, even when
`allow_external_on_host_sandboxes` lets others' tasks run.

The daemon itself refuses `-host-sandboxes` on a daemon others can
reach, unless `-allow-unisolated-host-sandboxes` is passed.

## How it is enforced

- **Fails closed at startup.** If the daemon can't read or parse the
  `-policy` file, it refuses to start. It never runs without the policy.
- **Before dispatch.** A task the policy refuses stays queued. Its row
  shows `waiting: local policy …`, naming the claim that was refused
  (`model.PolicyWaitCapability`). The first cycle after the policy (or
  the task) changes so that it fits dispatches it.
- **Before the sandbox.** The run checks the policy again on the task it
  read, before any sandbox is built or any code is cloned. A task edited
  between dispatch and setup is refused as a setup failure.
- **For every grain CI job.** A CI job is judged as the task it would
  be (`daemon.CheckCIJobPolicy`), before its capability grants are
  materialized and before its sandbox is built: the repository under
  test and every `repo-read:`/`repo-write:` it declares against `repos`,
  every capability it carries against `capabilities` (the job's own
  declarations included, whichever event runs it), and `open` -- every
  CI sandbox's network -- against `network`. Approval is not asked: a CI
  run is grain's own. A refused job errors with the policy's reason, and
  the daemon log says so in one line (`refused by local policy`).

With no `-policy` flag the daemon enforces nothing.

## Metadata carried through

The requester and approver are kept as pass-through metadata. The agent
is asked to end its commits with them, after `Origin:` and `Filed-by:`.
When grain writes a merge commit's message for lineage, they go there
too:

    Requested-by: someone@example.com
    Approved-by: lead@example.com

An approval made by grain itself (a schedule, auto-approval) names
nobody, so it adds no `Approved-by:` trailer.

## Single-tenant mode

A workstation's grain serves its owner. Nobody else signs in to it, and
this build has no other mode (the `-tenancy` flag is gone,
[single-tenant-reset.md](single-tenant-reset.md)):

- Its one credential is the
  **access code** it generates at first start
  (`<data-dir>/local-api-token`, mode 0600; `grain access-code` prints
  it). Every caller presents it -- a browser on the "Enter access code"
  page, the grain CLI as a bearer token -- and then reaches the UI and
  API as the daemon's own actor. Nothing is trusted for where it comes
  from: loopback and a network address grant nothing here. A session, Google sign-in, IAP
  assertion or identity header counts for nothing, so a caller without
  the code gets a 401 (the login page, for the UI). The code plus a URL
  that reaches the daemon is full owner access: treat it like a
  password, and `grain access-code rotate` it if it leaks.
  [access-code.md](access-code.md) has the details, including what host
  sandboxes can read. The routes that
  make or sign in to an account (`/api/auth/login`, `register`,
  `change-password`, Google sign-in, `POST /api/users` and a user's
  password) answer 404. The access-account allowlist that
  `/api/access/accounts` managed is gone (grain/task-4682). Who asked for a
  task comes from `-author` or the daemon's own actor (above).

A data directory that holds teams besides `default` or sign-in accounts is
refused at start (`model.Store.CheckSingleTenant`); a daemon still passed
`-tenancy` by an older unit names it among the flags it ignores.
