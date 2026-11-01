# Upgrading without stopping the work

> **Proposal; the substrate is decided.** Most of this has not been built.
> It is an ordered set of stages rather than one change. The first two
> stand alone, are small, and are worth having even if the rest is
> rejected. The substrate question that the previous version of this
> document left open is now settled: runs move into **controller-side run
> hosts** that share **one subscription-credential directory** (agreed
> with bsalmon in task 2700). The envd-in-the-guest route that version
> recommended is rejected, and *The substrate* below says why. What is
> still open is listed at the end.

## The question

grain upgrades itself, often, by restarting itself. An upgrade is a build
or an image pull, an install, a health check, and then `systemctl restart
grain-daemon.service` (`pkg/upgrade/upgrade.go`, `image.go`, `kube.go`) —
and the restart is where the work goes. On 18 September two upgrade
shutdowns between them cancelled seven live runs. Each was an agent an
hour into a task, a sandbox VM, and a conversation. All of it was thrown
away, the tasks were queued again from nothing, and each one took a strike
in its own failure streak for having been running at the wrong minute.

There is one answer for this today and it is a good one as far as it goes:
a **scheduled upgrade** holds the queue and waits until the deployment is
idle before it starts (`pkg/orchestrator/scheduledupgrade.go`). But "idle"
is not a state a busy deployment is often in, and that reconciler says so
in its own words. There is deliberately no deadline, so the wait can be
hours, and a standing Director session is already excluded from the busy
count because it would otherwise mean *never*. (`-upgrade-drain-window`
now lets an operator cap that wait, at the price of interrupting whatever
is still running when it expires.)

So the choice on the table is: **interrupt the work, or don't upgrade.**
This proposes a third.

## What a restart costs today

| what is in flight | where it lives | what a restart does | what it costs |
| --- | --- | --- | --- |
| an agent run | a goroutine in the daemon (`orchestrator.runOne`) holding the agent CLI as a subprocess and its forked `mcpserver`, killed as a process group (`pkg/procgroup`) | cancelled with the daemon's context. The row is finished `interrupted` (`model.InterruptedOutcome`) by the drain, or by the next startup if the drain ran out or the process was killed (`RecoverOrphanedRuns`, `pkg/orchestrator/recover.go`) | the whole conversation and the sandbox. An `interrupted` ending does not count toward the task's streak, whichever of the two finished it (`b660114c6`, `6e0b3c0e9`) |
| the sandbox that run was using | a granule VM, a container, or a pod | every VM under the prefix is deleted at startup (`GranuleSandboxes.ReapOrphans`, which is `SweepOrphans` with nothing yet in flight; `k8s_sandboxes.go` for pods) | a boot, a checkout, and whatever had been built in it |
| a grain CI run | `ci.Engine` goroutines in the daemon, one sandbox per job | stamped cancelled-and-interrupted at the next startup (`ci.StoredRunStore.RecoverUnattachable`; a run with a step to reattach is reattached instead, design/design/grain-on-gke.design.md#GKE-4). The task forgets the verdict and the branch is started again (`orchestrator.collectCIRun`) | the jobs that were in flight. The finished ones replay from the job cache, which is the one part of this that already works |
| a console or Director session | a process tree held by `pkg/console` | killed | somebody's terminal, mid-sentence |
| the UI, the API, the git proxy, the model proxy | one process, `--network host`, one bind each | connections refused for the length of the restart (measured per mode: `upgrade.GapRecorder`). Model-proxy tokens persist across it (`pkg/modelproxy/tokens.go`) | a `git push` in flight, an agent's model call; grain's own clients (`mcpserver`, the CLI, the UI's poll) wait it out |
| the merge queue, sync, schedules, branches, reviews | level-triggered reconcilers over the store | nothing: the next tick reaches the same conclusions | latency |

Two rows are already right, and they are the model for the rest. The
reconcilers are right because their state is in the store and a tick is
idempotent. The CI job cache is right because a finished job's verdict is
keyed by its inputs, so re-planning after a restart replays what was done
and pays only for what was actually in flight.

Everything else is wrong for one reason: **a unit of work's liveness is a
stack inside the daemon process**, so the process dying is the work dying.

Two smaller things are wrong in a way that is not structural:

* **The drain budget does not survive contact with the unit file.**
  `drainInFlight` waits up to `shutdownDrain` (4m30s, `pkg/daemon/run.go`)
  for runs to release their sandboxes. The container-mode unit gives the
  whole container `docker stop --time 30` and `TimeoutStopSec=60`
  (`scripts/setup.sh`), and both Kubernetes manifests set
  `terminationGracePeriodSeconds: 30` (`deploy/kubernetes/`). So the drain
  is cut off at thirty seconds and the remaining cleanup is left to the
  next startup.
* **An interrupted run leaves no conversation behind.** The session a task
  can be resumed into is read off `agent.Result` when `Run` *returns*
  (`pkg/orchestrator/run.go`). A run killed before it returns hands back
  no result, so the one mechanism that would make the loss cheap,
  `model.SuspendedSession`, is not written on exactly the ending where it
  matters most. claude's session id is known from the first event of the
  stream (`pkg/agent/claude/transcript.go` already tracks it as the stream
  arrives), but nothing writes it down until the end.

## What "non-disruptive" should mean

Three properties, in priority order. They are stated separately because
the cheap stages buy the first and only the expensive ones buy the third.

1. **Nothing in flight is lost.** An upgrade may stop a unit of work, but
   it may not throw away what that work has already done, and it may not
   hold the interruption against the task. A run whose conversation is
   resumed and whose task is re-queued without a strike has been
   *interrupted*; a run whose conversation is gone has been *destroyed*.
2. **An upgrade never waits on the work.** Pressing Upgrade on a busy
   deployment applies the upgrade in the minutes the build and restart
   take, not in however many hours it is until the queue empties.
   Wait-for-idle stays available as a policy; it stops being the only way
   to be kind.
3. **The gap is bounded, and clients retry through it.** A restart takes
   the API away for some seconds. Nothing a sandbox, a browser or an agent
   does in that window should become a failure; it should become a retry.

Non-goals, so the design is not judged against them:

* **Zero-downtime serving in the load-balancer sense.** Two daemons at
  once means two processes writing one embedded SQLite store
  (`pkg/model/store.go`, modernc) and two sets of reconcilers concluding
  things about the same rows. `SweepOrphans`' own doc comment adds a
  second reason: a deployment's granule state directory is its own, and a
  second daemon sharing it deletes the first one's VMs.
* **Live migration of an agent's own process.** The agent CLI is somebody
  else's program. We can keep it running across our restart, or start it
  again on the conversation it was in; we cannot move it.
* **Upgrading with no restart at all.** Settings are already re-read per
  tick; this is about the changes that are not settings.

## Requirements

Any design has to meet these three, and they are what decided the
substrate.

1. **Subscription logins keep working.** Agent CLIs authenticate with
   subscription logins, not API keys: claude through `~/.claude`, codex
   through an `auth.json` that it refreshes itself
   ([`codex-authentication.md`](../../docs/codex-authentication.md)). A run started
   by the old build and a run started by the new build must use the *same*
   login, at the same time, without one logging the other out.
2. **The login never enters a sandbox where repo code runs.** This is the
   rule [`codex-isolation.md`](../../docs/codex-isolation.md) is built around: the
   CLI runs on the controller, the model's file and command operations go
   through grain's MCP tools, and nothing the repository can execute can
   read the credential.
3. **It works on a single machine and on Kubernetes.** A single machine
   means both the host-binary install and container mode
   (`scripts/setup.sh`). Kubernetes means the one-replica, `Recreate`
   daemon of `pkg/upgrade/kube.go` with sandboxes as pods of their own
   (`k8s_sandboxes.go`).

## The substrate

Every serious version of properties 1 and 2 needs the same thing: **a
run's driver must not be a stack in the daemon.** The previous version of
this document weighed three ways to get there.

**(a) A per-run container ("grains").** The removed `pkg/grain` prototype used
a container per run holding the agent CLI, the VMM and the guest. The
controller creates, lists, tails and destroys it, and never calls in.
The code was removed in `601a53343` (18 September) as an unwired
prototype.

**(b) The agent under envd, inside the sandbox.**
[`envd-adoption.md`](../../docs/envd-adoption.md) makes E2B's `envd` the guest
agent, with a process API that can list and re-attach to what it started.
Running the agent CLI as an envd process would make a daemon restart
invisible to it.

**(c) A detached process on the host.** `setsid`, a transient scope, or
a sibling container, with stdout going to a file the daemon tails. In
container mode, anything that is a process inside `grain-daemon` dies
with it, so the only variant that works there is the sibling container,
which is (a) with fewer promises.

**(b) is rejected.** The agent CLI would run inside the guest, where the
repository's code runs, and it would have to authenticate from there. The
previous version proposed handing it a per-run model-proxy token instead
of the real credential. That does not survive requirement 1: a
subscription login is not an API key the model proxy can stand in for.
The CLI needs its own `~/.claude` or `auth.json` to talk to its provider
at all, and putting either in the guest breaks requirement 2. envd stays
the right answer for what it was adopted for, which is the sandbox's own
process and filesystem surface. It is the wrong place for the agent.

**The decision is a controller-side run host.** It is (a)'s shape, which
is also (c)'s sibling-container variant, with two changes from what
`grain.md` described: the run host sits on the controller's side of the
sandbox boundary rather than holding the VM, and it holds the
subscription login. The rest of this document is that design.

### Which substrate carries which CLI (container install with hostd)

> **Decided (task 1244): on a container install with grain hostd, a
> CLI that would run "in the container" runs through hostd instead,
> detached, from a *container-equivalent* allowlist entry.** That is
> option (b) of the task. Docker sibling run hosts (`-run-host-image`,
> option (a)) stay the substrate for Kubernetes and for a
> container install without hostd.

**Why it happened.** The upgrade of 2026-10-26 07:15 UTC on the
deployment that filed this work (release container, grain hostd,
`kubernetes-granule` sandboxes) kept one run and lost eight:

```
runhost: upgrade flushing; leaving run 1115-1's CLI running in grain hostd at cursor 308626
grain daemon: shutdown interrupted 8 run(s): 8 finished as interrupted ...
grain hostd adoption: 1 adopted, 0 lost, 0 unclaimed process(es) released
```

The survivor was an agy run. The Google edition puts agy on the host
through hostd, and that path is detached (hostd protocol 2,
[cli-run-location.md](cli-run-location.design.md), "Upgrades and host
runs"). The other eight had their CLI as a child of the daemon's
container (`agent.LocalHost`), so the container's stop
(`ExecStartPre=docker rm -f grain-daemon`) killed them. `setup.sh` never
passes `-run-host-image`, so on this install nothing but a host-run CLI
could survive. The eight runs' frameworks were not read from the store
when this was written: the task sandbox that wrote it could not reach
the deployment's state repository. They are taken to be claude runs and
the console session of task 859, because claude is the only other
framework in the deployment's rotation and a console session is never
host-run. The post-upgrade check now names each lost run's framework
(below), so the next upgrade records it.

The report also hid the loss. The adoption pass sees only run hosts and
hostd's processes, so the check said "1 adopted, 0 lost". It now also
counts every run that ended interrupted since the upgrade started
(`Store.InterruptedRunsSince`, `AdoptionReport.withRows`). A run whose
CLI stopped with the daemon is counted as lost for that reason, a run
still in setup as lost in setup, and the line totals them by framework
("1 adopted, 8 lost (lost by framework: claude 7, unknown 1)"). It also
says which lost runs the install's own run locations account for
(`orchestrator.Config.UpgradeInterrupts`, the notes the upgrade pane
shows): a claude run lost on a Cloudtop install, which keeps claude in
the container, is expected there, and only the rest are called a
regression.

**Why hostd and not docker run hosts, on this install:**

* **It is the path already proven here.** Detach, re-attach from the
  persisted cursor, `DecideHostd`, `RecoverHostd` and the
  `ResumeAdoptedRun` they share carried run 1115-1 across the very
  upgrade that lost the rest. The installer e2e
  (`tests/installer/zz_hostd_test.go`) restarts a daemon under a
  host-run CLI on every branch. A docker run host on a container install
  has been hand-run only with local container sandboxes reached through
  a socket bridge (task 3606). It has never run beside `granule` or
  `kubernetes-granule`.
* **It reaches every sandbox backend this install uses with nothing new
  mounted.** A hostd child runs as `$GRAIN_USER` on the host and sees
  `$GRAIN_DATA_DIR` and `$GRAIN_SANDBOX_DIR` at the paths the daemon
  sees (cli-run-location.md, "Same paths"). The granule exec key, the
  kubeconfig a `kubernetes-granule` `mcpserver` is handed
  (`-k8s-kubeconfig`), and grain's local API token are therefore already
  where the argv says. A docker run host would need the kubeconfig
  mounted. It sits in the data directory or `/var/lib/grain`, and a run
  host may be given whole directories only (`Spec.Validate`), so that
  means either the whole data directory, secrets included, or moving the
  kubeconfig. It would also need `kubectl` and any exec credential
  plugin that kubeconfig names inside the image. The host has both
  already, because a host-run agy's `mcpserver` uses them today.
* **The trust boundary does not move.** A run host would be a new
  container holding the subscription login. A hostd child is a process
  of the same user the daemon already runs as (cli-run-location.md,
  "Security": grain is trusted as the installing user). Requirement 2
  holds either way: the CLI stays on the controller's side of the
  sandbox and reaches the guest only through `mcpserver`.

**What "container-equivalent" means.** The entry must not turn a
container run into "the user's own CLI with the user's own login", which
is what `-claude-run=host` means today. It has to be the image's CLI,
with the deployment's login:

1. **The image's binary.** `setup.sh`'s extractor already copies the
   image's `grain` to the host on every daemon start
   (`write_host_extractor`). It extracts the image's `claude` and
   `codex` beside it into the same versioned directory
   (`/usr/local/lib/grain/hostd/<image-id>/`), dereferencing claude's
   `/usr/local/bin/claude` symlink (`docker cp -L`). An upgrade swaps
   the `current` link and never overwrites a running binary, so a run
   adopted across an upgrade keeps the CLI it started on, and the next
   run gets the new image's.
2. **The deployment's login, shared rather than copied.** In the
   container, `HOME` is `$GRAIN_DATA_DIR/home` (`docker_run_args`). The
   entry sets the same `HOME` (`set_env`), so `~/.claude`, codex's
   `CODEX_HOME` and the per-task session store are the same directory
   for the container and the hostd child, not copies (requirement 1).
   The OAuth token claude reads from grain's secret store
   (`CLAUDE_CODE_OAUTH_TOKEN`) is sent as it is to a container run. The
   entry's `env:` names it, and nothing else of the daemon's environment
   crosses. Today `buildClaudeFramework` sends no token to a hostd
   claude, because "host" meant the user's own login. The
   container-equivalent entry is a location of its own (`detached`,
   below) so that meaning is kept for `host`.
3. **Its own name in the allowlist** (`cli claude-detached`,
   `cli codex-detached`), so an operator's existing `claude`/`codex`
   host entries, which point at a CLI on the host's PATH, keep their
   meaning.
4. **A run location of its own.** `-claude-run=detached` and
   `-codex-run=detached` (and Settings → Models' "detached in grain
   hostd") select it. `setup.sh` makes it the default for claude when
   hostd is on. `container` stays available as the explicit opt-out, and
   is what a hostd that is down or incompatible falls back to, with the
   Settings warning `host` already gives.

**Coverage, by what runs a CLI on a container install with hostd:**

| what | substrate today | survives a daemon restart? | to close it |
| --- | --- | --- | --- |
| agy (Google edition) | hostd, detached | **yes** (adopted from the cursor) | — |
| claude run | hostd, detached, from `cli claude-detached` (the installer's default with hostd, `GRAIN_CLAUDE_RUN=detached`) | **yes** (adopted from the cursor; `tests/installer/zz_hostd_test.go`) | — (`container` is the opt-out, and the fallback while hostd is down, incompatible or lacks the entry) |
| claude run, Cloudtop install (`google/scripts/cloudtop-install.sh`) | `LocalHost` in the container: the installer passes `GRAIN_CLAUDE_RUN=container` before `setup.sh` picks its default, because a Cloudtop's exec policy blocks claude on the bare machine and `detached` execs the image's claude there | no: every claude run in flight is lost, re-queued with no strike. The check lists it as expected for this install (`AdoptionReport.Expected`), and Settings → Models and the upgrade pane say so, with the Google edition's caveat on detached | whether the exec policy lets hostd start `/usr/local/lib/grain/hostd/<image>/claude` has not been checked on a real Cloudtop (task 1506). If it does, the installer should default claude to `detached`. If it does not, claude needs a substrate the policy allows, such as a docker run host (`-run-host-image`), which on this install would need the kubeconfig and `kubectl` described above |
| codex run | hostd, detached, from `cli codex-detached` (`-codex-run=detached` / Settings → Models; `GRAIN_CODEX_RUN=container` is the installer's default until flipped) | **yes** when `detached` (adopted from the cursor; `grain codex-detached` serves the run's loopback MCP HTTP adapter alongside `codex.bin` outside the daemon; `tests/installer/zz_hostd_test.go`) | — (`container` is the opt-out, and the fallback while hostd is down, incompatible or lacks the entry) |
| console / Director session | a durable task: each turn is a `task_run` attempt on the session's framework (durable tasks DT-S10) | as its framework's runs do: **yes** for claude/codex `detached` and agy through hostd, adopted like any run; no for a CLI in the container | — for the CLI, which is the framework's row above. Its `chat` wait (up to 1 h) is a poll loop in the CLI's own `mcpserver` (`daemonChat`), which treats a failed read as a gap rather than the end of the turn and bounds each read by the wait's deadline, so a reply posted after the restart reaches it (`TestDaemonChatWaitsThroughADaemonRestart`). The check and the upgrade pane name each session run and count them (`AdoptedRunNote.Session`). |
| run in setup (sandbox building, no CLI yet) | the daemon's goroutine | handed back at shutdown: finished interrupted with no strike, queued at once with its Run now and priority kept (task 837's rule), its sandbox released (`finishStoppedInSetup`; it used to read as `setup-failed`, a strike) | — |

Kubernetes is unchanged: run pods (`-run-host-image=self`) are its
substrate, and `CheckRunHostBackend` still refuses `-run-host-image`
with `-container-sandboxes`.

## The design

### 1. Run hosts

The daemon splits in two.

* **The daemon is the control plane:** the store, the reconcilers, the
  UI and API, the git proxy and the model proxy. There is exactly one.
* **A run host is one run's driver:** the agent CLI and its `grain
  mcpserver`, nothing else. There is one per live run.

Where a run host is, by deployment mode:

* **Single machine, container mode:** one container per run, a *sibling*
  of `grain-daemon` started through the docker socket the daemon already
  mounts. It must not be a process inside `grain-daemon`, because the
  unit's `ExecStartPre=docker rm -f grain-daemon` kills everything inside
  that container on every restart. It uses the daemon's own image and
  network mode, so `mcpserver` reaches the daemon at the address it does
  today.
* **Single machine, host binary:** the same container wherever docker is
  present. A host without docker needs another unit of isolation outside
  the daemon's cgroup, such as a transient systemd unit per run. That is
  listed as an open question rather than designed here.
* **Kubernetes:** one pod per run, created through the same API access
  `k8s_sandboxes.go` already uses for sandbox pods.
* **Container mode with grain hostd:** a host CLI or `run: host`
  capability server started through hostd is a detached hostd process.
  It spools its output the way a run host does and outlives the daemon
  until its own deadline. hostd lists it, and the adoption pass decides
  it with the same table (`runhost.DecideHostd`). See
  [cli-run-location.md](cli-run-location.design.md), "Upgrades and host
  runs", which also covers draining hostd for its own upgrade.

Every run host carries labels (container labels, or pod labels and
annotations) that are the whole of what a new daemon needs in order to
recognise it:

| label | what it is for |
| --- | --- |
| task | which task's work this is |
| run | which `task_run` row claims it |
| generation | which build created it, for the post-upgrade report and for drain |
| protocol | the run-host protocol version, so a daemon can tell what it can adopt |
| deadline | the hard ceiling the run host enforces on itself, so a daemon that never comes back cannot turn a two-hour budget into unbounded spend |

The daemon **creates, lists and destroys** run hosts. It never parents
them: no run host is a child process of the daemon, and nothing about a
run host's lifetime depends on the daemon's. "Is this run alive?" is
answered by the listing, every tick, and the answer is the same for the
process that started the run and the process that replaced it.

The deadline label is a ceiling, not the budget. Docker labels cannot be
changed after creation, so the label carries the run's budget plus a
fixed adoption allowance. The daemon keeps enforcing the actual budget,
including the wait credit an adoption earns (`model.RunWaitCredit`), and
destroys the run host when that budget is spent. The label only matters
when no daemon is there to do that.

The agent CLI inside a run host keeps every restriction it has today:
codex's native tools stay off ([`codex-isolation.md`](../../docs/codex-isolation.md)),
and the model reaches the sandbox only through `mcpserver`. What the run
host holds that a sandbox must not is exactly the login, which is why it
sits on the controller's side.

**A run host does not inherit the daemon's environment.** Under
`agent.LocalHost` the CLI is a child of the daemon and gets
`os.Environ()` plus the run's own variables. In a run host it gets the
image's environment plus the run's own variables (`agent.Command.Env`,
the spool's `env` file), and nothing else. That keeps the daemon's
secrets out of the run host. The variables a framework promises an
operator are forwarded by name. For claude these are `MCP_TOOL_TIMEOUT`,
`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`, `CLAUDE_CODE_DISABLE_AUTO_MEMORY`
and `CLAUDE_CODE_MAX_RETRIES` (`operatorVars` in
`pkg/agent/claude/claude.go`). Nothing else on the daemon reaches the
CLI. That includes `ANTHROPIC_BASE_URL`, `HTTPS_PROXY`/`NO_PROXY` and any
other `CLAUDE_CODE_*` setting. A deployment that needs one of these in a
run host sets it in the run-host image.

This is close to the deleted grains design and should borrow from it
freely: `Grains.List` as both re-attach and reap, records rather than
calls, version skew with its own named refusal. The differences are that
a run host does not hold the VM (the sandbox stays where it is today) and
does hold a credential.

### 2. The shared credential directory

One directory holds the subscription logins and the per-task session
store (`pkg/agent/sessionstore`). Today these are spread across the
controller's own home (`~/.claude`) and its secrets directory (codex's
per-backend `codex-auth-*.json`, `pkg/agent/codex/auth.go`). They move
together into one directory, which is mounted **read-write, as a
directory**, into every run host of every generation, and into the daemon
so the UI can still upload and clear logins.

Two rules decide how it is mounted, and both are about refresh:

* **A directory, never single-file bind mounts.** A CLI that refreshes
  by writing a temporary file and renaming it over the old one replaces
  the file's inode. A bind mount of the single file pins the old inode:
  the run host keeps reading the token it started with, the refreshed one
  lands beside it unseen, and the run fails at the next expiry. A mounted
  directory sees the rename. (codex 0.153.0 happens not to rename: it
  truncates and rewrites `auth.json` in place, see
  [`codex-authentication.md`](../../docs/codex-authentication.md#several-processes-one-login).
  A directory is still the mount that is right for both kinds of writer.)
* **Never a copy per run host.** A copy refreshes on its own schedule.
  With refresh-token rotation, the first copy to refresh invalidates the
  refresh token every other copy holds, and every other run is logged
  out. One file, shared, is the only arrangement that survives rotation.

The session store moves with the logins for the same reason. A run on the
new generation resumes a conversation the old generation began, and a
run the new daemon refuses to adopt still leaves its conversation where
the next dispatch will find it. That is property 1 even on the refusal
path.

Where the directory lives:

* **Single machine:** a host directory, for example
  `/var/lib/grain/data/agent-home`, owned by the grain user and mounted at the
  same path into `grain-daemon` and into every run host.
* **Kubernetes, the default:** a `ReadWriteOnce` PVC, with run pods
  pinned to the daemon's node by pod affinity
  (`requiredDuringSchedulingIgnoredDuringExecution`, topology key
  `kubernetes.io/hostname`). `ReadWriteOnce` is per *node*, not per pod,
  so co-located pods share it read-write; only `ReadWriteOncePod` forbids
  that. This is cheap and works on any cluster's default storage class.
  The pinning costs little because run pods are light: the heavy work is
  in sandbox pods, which keep scheduling anywhere.
  Because the claim stays attached to that node while any run pod uses
  it, the daemon must come back on the same node. The upgrade
  (`pkg/upgrade/kube.go`) therefore pins the new daemon pod to the node
  the old one ran on when it patches the Deployment. A node that is
  drained or lost takes its run pods with it, and the next daemon
  finishes their rows with that reason (piece 4).
* **Kubernetes, the option:** a `ReadWriteMany` volume (GKE Filestore
  CSI, or NFS) for clusters that need run pods spread across nodes.
  **Do not use a gcsfuse-backed volume:** renames there are not atomic,
  and in-place rewrites there are not coherent between readers either.

### 3. Token refresh: the CLI's job, except codex's

Where the CLI can do it safely, grain leaves the refresh of a
subscription token to the CLI, as happens when a person runs it by
hand. What grain owes is a layout in which that refresh is safe. codex
cannot do it safely, so grain does it for codex (below).

* **claude:** the recommended login for a deployment is a long-lived
  token from `claude setup-token`, passed as `CLAUDE_CODE_OAUTH_TOKEN`
  (`pkg/agent/claude/claude.go`, `WithOAuthToken`). It never refreshes,
  so any number of run hosts can hold it at once. `~/.claude` is still
  shared, for sessions and settings.
* **codex:** several run hosts refreshing one `auth.json` at the same
  moment is **not safe** with the pinned CLI (0.153.0). There is no
  cross-process lock, so two processes can spend the same rotating
  refresh token. On codex's 401 path the loser's run fails. On its
  proactive path the loser recovers by re-reading the file, but OpenAI
  still sees a reused refresh token. The measurement and the source
  walk-through are in
  [`codex-authentication.md`](../../docs/codex-authentication.md#several-processes-one-login).
  The fallback therefore applies: grain refreshes the login on a
  schedule, ahead of expiry, so that no CLI ever reaches the point of
  refreshing it. The refresher (`codex.Refresher`) takes a lock file in
  the shared credential directory, so only one generation renews a given
  login. See
  [`codex-authentication.md`](../../docs/codex-authentication.md#grain-renews-the-login-itself).

### 4. Run hosts never touch the store

SQLite has one writer, so there is only ever one daemon, and a run host
never opens the store.

* **A run host appends its events and transcript to a spool file** on a
  volume of its own that the daemon can also read: a per-run directory
  under the data directory on a single machine, and a per-run directory
  on a volume beside the daemon's on Kubernetes (the same node pinning
  applies). Never in the credential directory.
* **The daemon tails each spool from a persisted cursor.** The cursor is
  written as the spool is read, so a new daemon resumes where the old one
  stopped. The adopted run's transcript has neither a hole nor a
  duplicate.
* **Adoption on startup** replaces today's blanket
  `RecoverOrphanedRuns`, whose own doc comment says it can only be a
  startup sweep because nothing distinguishes an abandoned row from a
  live one. The run-host listing is that distinction. A new daemon:
  1. lists run hosts;
  2. matches each one to a live `task_run` row by its run label;
  3. resumes tailing each matched spool from its cursor;
  4. credits the time it was away as wait time, not run time;
  5. releases `UpgradeHold` (`ReleaseUpgradeHold`, already a startup
     pass);
  6. destroys any run host no live row claims;
  7. finishes every live row whose run host is gone, with the reason the
     listing or the spool gave rather than a guess.
* **Sandbox reaping goes by claim, not by prefix.** `ReapOrphans` is
  `SweepOrphans` at startup, when the two definitions coincide *because
  nothing is expected to survive*. Once runs are adopted, startup has to
  use the claim-based definition, read from the store and the listing
  rather than from memory.

### 5. The proxies survive the gap

An adopted run authenticates with what the previous daemon minted, so
nothing it holds may live only in that daemon's memory.

* **Git access survives already.** A sandbox may touch the repositories
  its live run was dispatched against (`pkg/gitproxy/authorize.go`), and
  the tokens are a file (`secrets/sandbox-tokens.json`). Adopt the row
  and the run's access is what it was.
* **Model-proxy tokens survive too.** `pkg/modelproxy/tokens.go` used
  to keep them in memory only, on the premise that a token is minted for
  a run a restarted daemon has already lost; once runs are adopted that
  premise is false. They are now persisted
  (`secrets/modelproxy-<vendor>-tokens.json`) as the id and a hash of the
  secret, which is enough to verify a presented token, so the disk still
  never holds a usable credential.
* **Our own clients retry.** `mcpserver`'s calls to the daemon go
  through `pkg/ui/httpclient.go`, which waits out a restart for up to two
  minutes and then says the daemon is restarting. The UI's poll shows
  "grain is restarting" when the daemon said it was
  (`GET /api/config`'s `restarting`) before it stopped answering.

The clients we do not own are `git` inside a sandbox and the agent CLI's
own model calls. A small long-lived front door that holds connections
across the restart would cover them. It stays conditional on Stage 1's
measurement of how long the gap actually is: one more process to
supervise is worth it only if that gap hurts.

### 6. The upgrade protocol

1. **Stage the build.** Build or pull, health-check the artefact, decide
   the schema step. Unchanged, and still the part that fails harmlessly.
2. **Hold dispatch.** `orchestrator.UpgradeHold`: no *new* run starts
   while the daemon is about to be replaced. Unchanged.
3. **Flush cursors.** Stop tailing, write every spool cursor, stop
   writing the store. Seconds: nothing waits on the work, only on the
   daemon's own state reaching disk.
4. **Restart only the daemon.** Run hosts are not signalled. They keep
   working with no controller, writing to their spools.
5. **Adopt.** The new daemon runs the adoption pass of piece 4.
6. **Release the hold.** Dispatch resumes, and the post-upgrade check
   (`orchestrator/upgradecheck.go`) adds how many runs were adopted, how
   many were lost, and why.

**New run-host images apply only to new runs.** An old run host keeps the
build it started with until its run finishes. A mixed fleet is the
ordinary state during a drain, which is a blue/green drain with no
deadline of its own beyond each run's budget.

**Version skew is never a stranding.** A daemon that meets a run host
whose protocol label it does not know either adopts it, if the protocol
is one it supports, or finishes it legibly: destroys it, finishes the row
`interrupted` with a reason naming the protocol, and leaves the
conversation in the shared session store for the next dispatch. A run
host is never left running unwatched. A **rollback** follows the same
rule: the build rolled back to meets run hosts the newer build created,
and adopts or finishes them in exactly this way.

Two compatibility rules come with this:

* **Skew is N±1.** A daemon adopts run hosts of the previous protocol,
  and, because a failed health check rolls back
  (`upgrade.Config.HealthCheckArgs`), refuses those of the next one by
  name rather than misreading them.
* **Schema changes are additive within an adoption window.** The upgrade
  already knows the build it cuts over to may expect a different schema
  (`pkg/upgrade/schema.go`). No destructive migration ships in the same
  release as a run-host protocol change, so a rollback meets a store it
  can still read.

### 7. The default policy

Once Stage 2 ships, the proposed default is **upgrade now and adopt the
work**. Wait-for-idle (`pkg/orchestrator/scheduledupgrade.go`) stays as an
option, for an operator who wants a clean boundary, such as before a
destructive migration. This is open question 3.

## The stages

Each is independently shippable and independently valuable.

| stage | what lands | buys | rough size |
| --- | --- | --- | --- |
| 0 | an interrupted run is not a failed one, keeps its conversation, and drains inside the time it is actually given | property 1, weakly, for agent runs | small, landed |
| 1 | clients retry through the gap, model-proxy tokens survive it, and the gap is measured per mode | property 3, mostly | small–medium |
| 2 | run hosts, the shared credential directory, spools and adoption, for agent runs: single machine first, then Kubernetes | properties 1 and 2 for agent runs | large |
| 3 | CI job drivers survive a daemon restart; CI job cache keys on the sandbox image digest rather than wiping on every daemon revision | properties 1 and 2 for CI | medium, landed |

### Stage 0 — stop charging the task for our restart

> **Landed.** `errDaemonStopped` and `recoverRun` both finish runs as
> `model.InterruptedOutcome`, with a note on the task;
> `agent.RunConfig.OnResumableSession` records the session from the
> stream (`orchestrator.earlySession`); the stop timeouts were raised to
> `shutdownDrain` plus slack, held there by
> `TestStopTimeoutsCoverTheShutdownDrain`. What follows is the plan as
> it was written.

1. **`interrupted` excluded from `task_streak`.** Landed in `b660114c6`:
   `model.InterruptedOutcome`, recorded on the shutdown path and skipped
   by the streak computation and `FailureStreak` beside `PausedOutcome`
   and `CapacityOutcome`. What is left is `recoverRun`: a row the drain
   could not finish is still `orphaned` at the next startup, and that
   still counts.
2. **Record the session id as soon as it is known**, not when the run
   returns. The claude transcript parser knows it from the first event.
   Record it against the task the moment it is known
   (`SetSuspendedSession`), so a run killed mid-turn still leaves a thread
   for its next dispatch to resume. codex and antigravity reach the same
   through `pkg/agent/sessionstore`'s per-task homes.
3. **Make the drain budget agree with the stop timeouts.** `docker stop
   --time`, `TimeoutStopSec` and `terminationGracePeriodSeconds` at least
   `shutdownDrain` plus slack, or `shutdownDrain` lowered to what they
   allow. One or the other, rather than the current disagreement.

### Stage 1 — retry through the gap

1. **Retry with backoff.** `mcpserver` and the CLI go through
   `pkg/ui/httpclient.go`, whose `RetryPolicy` waits a daemon out for up
   to two minutes. A refused dial, and a relay 502/503 marked
   `Grain-Relay-Not-Forwarded` (no daemon connected, or no stream to it),
   are retried for any method: the request never reached grain. A reset,
   an EOF, or any other gateway 502/503/504 is retried only for an
   idempotent method. The relay answers 502 for a tunnel that dropped
   after the daemon read the request, and IAP's load balancer answers
   502 or 503 for a backend that closed mid-request, so a bare status
   does not prove the request went unhandled. Grain's own JSON 502/503
   answers are not retried at all. A call that outlasts the budget fails with
   `ui.DaemonUnavailableError`, a sentence naming the restart. The UI's
   poll already kept a standing banner instead of error toasts while the
   API is down; `GET /api/config` now says `restarting` once the daemon
   is told to stop or while an upgrade runs, and a disconnection that
   follows says "grain is restarting".
2. **Model-proxy tokens are persisted** (piece 5): each vendor's registry
   is `secrets/modelproxy-<vendor>-tokens.json`, mode 0600, holding each
   token's id, a SHA-256 of its secret, its owner and its expiry -- what
   is needed to check a presented token and nothing that can be presented
   as one. `pkg/modelproxy/tokens.go`'s doc comment says so.
3. **The gap is measured** by the daemon itself (`upgrade.GapRecorder`,
   `state/restart-gap.json`): the outgoing process records when it closed
   the API, the git proxy and each model proxy (or, if it was killed
   first, when it was told to stop; a process killed without being told to
   stop first, `kill -9` or an OOM kill, leaves no time to measure from, and
   the next start logs no gap), the incoming one when it bound each
   again. Each gap goes to the journal as the listener comes up, tagged
   host binary, container or kubernetes, and the post-upgrade check's
   task reports it. `tests/container` measures the container case on
   every image build. The numbers decide whether the front door is ever
   built.

**Git in a sandbox does not tolerate the gap.** git has no retry for a
refused connection: `git fetch` or `git push` against the proxy while it
is down fails at once with "Failed to connect ... Couldn't connect to
server" (checked against git 2.39). `http.lowSpeedLimit`/`lowSpeedTime`
do not help -- they abort a stalled transfer, and a refused connection
never starts one -- and a credential helper runs only after a connection
has been made. Today the agent sees that error and retries the command
itself. If the measured gap makes that common enough to matter, the
remedies are the front door of piece 5 or a retrying wrapper around git
in the sandbox image; neither is part of this stage.

### Stage 2 — run hosts and adoption for agent runs

> **Partly landed (single machine, task 2990).** `pkg/runhost` holds the
> run-host contract: labels and protocol (`runhost.Labels`,
> `runhost.Supported`, N−1), directory-only mounts (`Spec.Validate`), the
> spool and its entrypoint (`grain runhost exec`, `runhost.Exec`, which
> enforces the deadline label itself), the docker driver
> (`runhost.Docker`), an `agent.Host` that launches a run into a run host
> and tails its spool (`runhost.Host`, with `Attach` as the adopting
> half), and the adoption decision as a pure function (`runhost.Decide`).
> The cursor is `task_run_spool_cursor`. `-run-host-image` (with
> `-run-host-credential-dir`) drives **claude** runs in run hosts; codex
> stays under `LocalHost` until grain renews its shared login itself
> (open question 1). At startup `orchestrator.RecoverRunHosts` frees
> unclaimed run hosts and finishes rows whose run host is gone, refused
> or past its deadline with that reason. **Adoption (task 3281):** a
> stopping daemon detaches from its run hosts (`runhost.Host.Detach` is
> `orchestrator.StoppedByDaemon`), leaving each row live, its leases held
> and its sandbox standing. The next daemon claims each matched run's
> sandbox back (`SandboxReadopter`, before the startup reap, which now
> spares exactly those). It then carries the run to its end in
> `orchestrator.ResumeAdoptedRun`, which attaches from the persisted
> cursor and parses the spool through the framework
> (`agent.ResultParser`). It reads the outcome with the same rules and
> makes the same writes as `RunDispatch` (`classifyRunEnd`,
> `recordRunEnd`), and opens the pull request as `runOne` does
> (`afterAgentRun`). The deadline is carried from where the run's own
> clock started, its agent's first turn (`agent_started_at`, read by
> `LiveRuns`; `adoptedClockStart`), and the gap is credited as wait time
> (`newAdoptedRunClock`). **Stage 2c gaps closed (task 3526):** the leases
> a run mints while it sets up are recorded against its row
> (`Store.RecordLeases`), and the adopter revokes every lease recorded
> for the run when it ends (`revokeRecordedLeases`), as `RunDispatch`
> does. A run that retried before the restart keeps its earlier attempts
> in its stored transcript: the adopter splits them off the live
> transcript (`earlierAttempts`), by the head of the stream `Attach`
> returns for the attempt in flight -- a grain hostd spool is not on the
> daemon's disk, so the split is made after the attach, not before it
> (task 1243) -- and claude parses the whole run
> (`agent.AttemptsParser`). A granule VM under an address pool is
> readopted: the address its container holds is reserved in the new
> process's pool (`sandboxaddr.Pool.ReserveRoot`). `K8sSandboxes` and
> `K8sGranuleSandboxes` readopt their pods.
> **The upgrade protocol (stage 2c, task 3282).** After the build is
> staged and before `RestartCmd`, an upgrade calls `upgrade.Config.Flush`.
> The daemon wires that to `runhost.Host.Flush`, which stops every tail,
> persists its cursor and waits for each run to hand back `ErrDetached`.
> The run hosts are not signalled. A restart that leaves the process
> running calls the flush's resume before the hold is released. It
> re-runs the adoption pass in the same daemon (`adoptRunHosts`). The
> Kubernetes path flushes inside staging instead, because its restart is
> already under way when staging ends (stage 2d, below). The startup
> pass builds an `orchestrator.AdoptionReport`, and the post-upgrade
> check reports from it how many runs were adopted, how many were lost
> and why, and how many run hosts were freed.
> `TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest` (`cmd/grain`,
> in the "run host" CI job) is the acceptance test against a real daemon
> and real docker. It found that adoption had never worked in a real
> daemon: the pooled `daemon.BackendFramework` was not an
> `agent.ResultParser`, so every adopted run was finished as interrupted
> ([`test-runs/2026-10-13-daemon-restart-adoption.md`](../../docs/test-runs/2026-10-13-daemon-restart-adoption.md)).
> The walkthrough against the real `grain daemon` binary found that a real
> claude CLI could not start in a docker run host. Its `--mcp-config` was
> in the daemon's `/tmp`, and its `HOME` was `/`. It also found that the
> restart gap credited a quiet run's silence before the stop. All of
> these are fixed: `Host.CreateTemp` uses the staging root, `HOME` is the
> credential directory, and a detach always persists the cursor
> ([`test-runs/2026-10-14-run-hosts-daemon.md`](../../docs/test-runs/2026-10-14-run-hosts-daemon.md)).
> **Stage 2d (task 3698).** The Kubernetes path now flushes as well. Its
> restart has no command, because repointing the Deployment starts it,
> so the flush comes inside staging (`upgrade.Upgrader.stageKube`):
> after the checks, before the rows are exported and the Deployment is
> repointed. The tails are stopped before the rollout begins, not when
> the pod's termination reaches them. If the export or the repointing
> fails, the runs are resumed. When the Deployment already runs the
> target image, `kubectl set image` rolls out nothing and no new pod
> would adopt the runs, so it does not flush (`kubeFlush`). The pod's
> termination still detaches anything the flush missed.
> **Descendants of a readopted granule VM are not readopted, by
> decision.** `ReserveRoot` reserves only the root's family, so the
> startup reap destroys any sandbox created beneath a readopted VM.
> Nothing creates one yet: `GranuleSandboxes.AcquireUnder` has no caller
> and no agent-facing route. Readopting descendants would need records
> that do not exist. A child's container does not say who its parent
> is, a name has no room for it (`sandboxaddr`'s package comment), and
> the run's handle to the child lives only in the old process. Whatever
> adds that route has to add three things together: a label on the
> child's container naming its parent, a `Pool` reservation of a child
> at the address it holds (the `AcquireChild` counterpart of
> `ReserveRoot`, built from those labels, parents first), and a
> `Readopt` of the family. Until then, a child lost to a restart is
> lost the same way it would be to the run's own release.
> **The single-machine hand run is done (task 3606).** A real claude CLI
> on a real model ran an implement task in a run host under a systemd
> `grain-daemon`. The daemon was upgraded in place (source mode) while a
> tool call was in flight, and separately restarted twice with
> `systemctl restart`. Each time the run host kept running, the journal
> logged `flushed the spool cursors of 1 run(s)` and `run-host adoption:
> 1 adopted, 0 lost`, and the transcript had no hole. The run kept its
> `StartedAt` and deadline, and the new build opened its pull request.
> The check task listed it as adopted. It found that under a run host,
> claude's `--mcp-config` named the daemon's own binary rather than the
> run host's `/usr/local/bin/grain`, so a daemon installed anywhere else
> started every run with no tools. That is now fixed. What it stood in
> for: a model-proxy credential instead of a subscription login, and
> local container sandboxes reached through a docker socket bridge
> instead of granule sandboxes
> ([`test-runs/2026-10-15-upgrade-adopts-live-claude-run.md`](../../docs/test-runs/2026-10-15-upgrade-adopts-live-claude-run.md)).
> **The cluster acceptance test is written, and does not pass yet
> (task 3858).** `TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest`
> (`cmd/grain`, gated on `GRAIN_RUNHOST_KIND`) runs the real daemon on
> kind with `-k8s-sandboxes` and `-run-host-image=self`. It rolls the
> Deployment through an Upgrade when a local registry is given
> (`GRAIN_RUNHOST_KIND_REGISTRY`), and through `kubectl rollout restart`
> otherwise. It found that a claude run cannot reach a Kubernetes sandbox
> at all. `k8sSandbox` gives `agent.RunConfig` no sandbox target, and
> `grain mcpserver` has no flag for a pod. So no run gets as far as a run
> pod, and the flush, `Readopt` and `ResumeAdoptedRun` are still untested
> on a cluster
> ([`test-runs/2026-10-15-k8s-daemon-roll-adoption.md`](../../docs/test-runs/2026-10-15-k8s-daemon-roll-adoption.md)).
> **Since then (DT-S3, 2026-10-26):** the reachability gap is closed
> (`agent.RunConfig.K8sPod`), and the test passes with
> `kubectl rollout restart` for a durable task. So does its no-run-host
> twin, `TestDaemonRollResumesADurableRunItDidNotAdopt`
> ([`test-runs/2026-10-26-k8s-durable-roll.md`](../../docs/test-runs/2026-10-26-k8s-durable-roll.md)).
> Not yet done: the Upgrade path on a cluster, whose health check failed
> in kind with "the check pod is Missing", and the hand run on
> Kubernetes.
> The run-host walkthrough against the real `grain daemon` binary (task
> 3553) found that a run host cannot reach a local container sandbox:
> its `mcpserver` runs every tool as `docker exec`, and a run host has no
> docker socket. `-run-host-image` with `-container-sandboxes` is now
> refused at startup (`daemon.CheckRunHostBackend`), and Settings does
> not offer the container backend while run hosts are on (task 3730,
> open question 6).

> **Kubernetes driver landed (task 2991).** `runhost.Kubernetes` creates,
> lists and deletes one run pod per run through `pkg/k8s`, the client
> sandbox pods use. What was decided, and why:
>
> * **Labels.** A pod carries task, run, generation, protocol and the
>   deadline (Unix seconds) as label-safe labels for selection, and the
>   exact values (RFC 3339 deadline, spool path) as annotations, which
>   `List` reads. Its component is `runhost`, so the sandbox sweep never
>   matches it.
> * **Mounts are subPaths.** Each directory a run host is given is a
>   subPath of a claim the daemon pod mounts (`-k8s-run-host-volume
>   CLAIM:PATH`), at the same path. A run pod never sees the rest of the
>   data volume, which holds the store and the secrets.
> * **ReadWriteOnce, the default.** Run pods are pinned to the daemon
>   pod's node by a required `podAffinity` on `app.kubernetes.io/component:
>   daemon`. The pin is IgnoredDuringExecution, so on a rollout the run
>   pods stay on the old node and keep the volume attached there. The
>   replacement daemon pod has to come back to that node to mount the
>   volume, and `grain.yaml` brings it with a *preferred* podAffinity
>   (weight 100) towards run pods. It is not required, so that a lost
>   node does not leave the daemon unschedulable. If the scheduler puts
>   the daemon elsewhere anyway (the node is full or cordoned), the
>   daemon pod waits on a Multi-Attach error until the run pods there
>   end (by their deadline labels at the latest), and then finishes
>   their rows from their spools. If the node is lost, its run pods go
>   with it, and the next daemon finishes their rows with that reason.
>   `ReadWriteOncePod` is refused at startup (`runhost.CheckClaim`).
> * **ReadWriteMany, the option.** Use `-k8s-run-host-rwx` with a claim
>   like `deploy/kubernetes/runhost-rwx.yaml`'s (Filestore or NFS).
>   Nothing is pinned, and every run-host directory must be on that
>   claim. A gcsfuse-backed claim is refused by the daemon (provisioner
>   annotation or class name) and by `grain init
>   -k8s-run-host-rwx-class`, which reads the class's provisioner.
> * **The spool is on the shared claim, not an emptyDir.** The daemon
>   pod cannot read another pod's emptyDir. An emptyDir also dies with
>   its pod, which would take `exit.json` with it: a run that finished
>   during the gap would look the same as one that was killed. A
>   per-run directory under `-run-host-dir` on the claim survives both,
>   and is never the credential directory.
> * **Grace periods.** The daemon pod keeps 300s (stage 0's
>   `shutdownDrain` plus slack), because runs outside run hosts (codex,
>   antigravity) still drain. A run pod gets 30s
>   (`runhost.DefaultKubeGracePeriod`). It never drains, and on SIGTERM
>   it only kills the CLI and writes `exit.json`.
> * **Reaching the daemon.** A run pod is not the daemon pod, so its
>   `mcpserver` is given `-run-host-daemon-url` (the Service), which
>   outlives any one daemon pod. It runs under the daemon's service
>   account, because it execs into sandbox pods (`mcpserver -k8s-pod`,
>   over `mcp.KubectlExecRunner`). That transport did not exist until the
>   10-15 kind walk: before it, every CLI-framework run on
>   `-k8s-sandboxes` failed at dispatch, run host or not. The Service
>   also answers the run pod's mcpserver 401, since nothing authorizes a
>   caller that is not on loopback; the walk had to trust the pod CIDR
>   with `-ui-trusted-cidrs`, which trusts sandbox pods too
>   ([`test-runs/2026-10-15-k8s-run-hosts-sandboxes.md`](../../docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md),
>   F1 and F4).
>
> `TestKubeRunHostSurvivesADaemonRoll` runs a stand-in daemon as a
> Recreate Deployment on kind and rolls it mid-run. The run pod
> survives, and the next daemon pod adopts it from the cursor with no
> hole in the transcript
> ([`test-runs/2026-10-13-k8s-run-host-roll.md`](../../docs/test-runs/2026-10-13-k8s-run-host-roll.md)).
> It is not a grain CI job, because kind in a CI sandbox is the reason
> the real-Kubernetes job is out of `tests.yml`.
>
> Still missing before a real task finishes its pull request across a
> roll (items 1 and 2 have since closed -- the 10-15 kind walk finished
> a real task's pull request across `kubectl rollout restart`, with the
> sandbox pod claimed rather than reaped -- and the run pod's API
> credential, F4 of that walk, is still open):
>
> 1. The post-processing resume that 2a also lacks: the stopping daemon
>    still destroys its run hosts (`Host.Detach` is unset), and
>    `RecoverRunHosts` still finishes a matched run as interrupted.
> 2. Sandbox reaping by claim (`K8sSandboxes.ReapOrphans` still deletes
>    every sandbox pod at startup).
> 3. ~~A git-proxy address that survives the daemon pod.~~ Sandbox pods
>    are handed the `grain-sandbox` Service's cluster IP for the git proxy
>    and the model proxies (`-k8s-sandbox-service`), on ports the
>    `-sandbox-port-base` block fixes, instead of `$GRAIN_POD_IP`. An IP
>    rather than the Service's name, so a sandbox with no network, which
>    can reach the daemon pod and not kube-dns, needs no resolver. The
>    package and Actions caches, and the package cache's DNS
>    interception, are still on the pod IP: the package cache tells a
>    sandbox with no network apart by its source address, and the
>    interception binds 53/80/443 on an address the pod owns. After a
>    roll an adopted sandbox loses those, but it does not lose its push.

Single machine first, because it is this deployment and the cheaper
harness; Kubernetes second, with the PVC, pod affinity and node pinning
of piece 2. The order within each:

1. the shared credential directory, with today's in-daemon runs moved
   onto it. This is worth having alone, because it is where the codex
   refresh question gets its answer;
2. the run-host contract (create, list, destroy, labels) and the spool
   with its cursor;
3. runs dispatched into run hosts;
4. adoption on startup, replacing `RecoverOrphanedRuns` and prefix
   reaping;
5. the upgrade protocol of piece 6, and the default of piece 7.

### Stage 3 — CI job drivers

> **Landed (task 1245).** Each CI step with a `StepID` runs inside its
> sandbox under a detached step runner (`pkg/orchestrator/shell.go`) that
> spools its stdout/stderr to `$HOME/.grain-ci-step/<step-id>/output` and
> writes its exit code to `exit` inside the sandbox, independent of the
> `ssh`, `docker exec` or `kubectl exec` client that started it. When the
> daemon flushes for an upgrade or shuts down (`ci.Service.Detach`), the
> engine leaves the detached runner and its sandbox standing rather than
> cancelling the run or releasing the sandbox. On startup (or upgrade
> resume), `daemon.AdoptCIRuns` reads the live CI runs from the store,
> reconstructs their workflow plans from `state/ci-plans`, claims each
> active job's sandbox back through `orchestrator.SandboxReadopter` before
> `ReapOrphans` runs, verifies that the in-flight step's detached runner
> directory is present in the sandbox (`HasStepRunner`), and resumes the
> run in place (`ci.Service.AdoptRun`): finished jobs replay from the run
> row, the in-flight job re-attaches to its spooled step output from where
> it was, and downstream jobs waiting in `needs:` run once it completes.
> `StoredRunStore.RecoverUnattachable` (formerly `RecoverInterruptedExcept`) marks only the runs that could
> not be adopted as `interrupted`, and the post-upgrade check reports
> `"N CI jobs adopted, M lost"` beside the agent-run counts. (Merge
> batching has since been removed from the orchestrator, so there is no
> batch run left to resume.)
>
> **Job cache across upgrades.** `ci.Fingerprint` (version 4) keys each job
> on `ci.CanonicalSandboxImage(req.SandboxImage)` — the immutable
> `@sha256:...` digest when present, stripping the mutable `:sha-<commit>`
> tag that moves on every commit build — and omits `SandboxImage` for
> `Builtin` jobs (`state-check` hashes `GRAIN_STATE_CHECK_BUILD` in its own
> step environment instead). `CiJobCache` no longer wipes `state/ci-cache`
> when the daemon binary revision changes, so repositories whose CI does
> not change sandbox digest keep their entire job cache across daemon
> upgrades.

CI job drivers survive a daemon restart in the same way. The job cache already
replays finished jobs, so what this adds is that a job in flight survives
the restart too. Console and Director sessions were out of scope until
then, on the grounds that a person sitting in front of one can reopen it.
That is no longer the plan on a container install with hostd: a person
mid-conversation is the work most visibly lost, and the session's CLI
goes through the same detached hostd entry as a run's ("Which substrate
carries which CLI", above).

### Acceptance test

> With an agent run in flight, restart the daemon. The same run finishes
> and opens its pull request; its transcript has no hole; its deadline is
> not reset.

Behind that sentence: the run host is not restarted, its sandbox is not
reaped, its git and model-proxy access survive, and the pull request is
opened by the build that did not start the run.

## How it is proved

* **Table tests** for the adoption decision, one row per combination of
  live row, listed run host, protocol version and spool state. `pkg/model`'s
  state tests are where the outcome and streak rules are already held to
  one answer.
* **A container test.** `tests/container` already has the shape
  (`schema_upgrade_test.go` drives a real schema-changing upgrade): start
  a run, restart the daemon under it, and assert the acceptance test. A
  second case refreshes a login in the shared directory mid-run and
  asserts both generations see it.
* **A grain CI job**, so it is checked on every branch rather than by
  hand. `.grain/workflows/tests.yml`; it needs a sandbox backend, so it
  belongs beside the existing KVM job.
* **A hand run** in `docs/test-runs/`, on a real deployment in each mode,
  with a real agent mid-task on a subscription login.

## What this deliberately does not fix

* A run whose agent CLI dies for its own reasons during the gap is
  finished by the adoption pass, one tick later. Its conversation is in
  the shared session store, so it is interrupted rather than destroyed.
* A restart during the seconds a run host is being *created* still loses
  that run. It becomes a queued task again, which is the cheapest failure
  here.
* Two daemons never serve at once, so a client that cannot retry sees an
  error. That is what the front door is for, and it is conditional on
  measurement.
* On Kubernetes with the default `ReadWriteOnce` volume, losing the
  daemon's node loses its run pods. `ReadWriteMany` is the option for a
  cluster where that matters.
* The lifecycle reducer's serving sandboxes
  ([`task-lifecycle-sandbox.md`](../../docs/task-lifecycle-sandbox.md)), when they
  exist, are cold-started rather than adopted. That design already says
  so under *Host restart*.

## Open questions

1. **Is concurrent refresh of one codex `auth.json` safe?** No, not
   with codex 0.153.0
   ([`codex-authentication.md`](../../docs/codex-authentication.md#several-processes-one-login)).
   grain refreshes ahead of expiry on a schedule so the CLIs never need
   to (`codex.Refresher`, which the daemon and `grain executor` already
   run, with a lock file per login in the credential directory). Still open: whether
   OpenAI revokes the whole token family when it sees a reused refresh
   token.
2. **Persist model-proxy tokens, or re-mint them on adoption?** Persisting
   a hash keeps the disk free of usable credentials and is less code;
   re-minting keeps the registry in memory but has to write into the
   sandbox during adoption.
3. **Is "upgrade now, adopt the work" the default once Stage 2 ships?**
   Proposed yes, with wait-for-idle as the option.
4. **How long may adopted work go unsupervised?** The deadline label
   bounds spend. Is a shorter "no daemon has read my spool for N minutes"
   stop in the run host also wanted? (`grain-options.md` declined a lease
   deliberately; this is the case that reopens the question.)
5. **What isolates a run host on a host-binary install with no docker?**
   A transient systemd unit per run is the obvious candidate, but the
   daemon runs as an unprivileged user there, and per-run labels would
   need a home of their own.
6. **What does a run host need in order to reach its sandbox?** `mcpserver`
   reaches granule VMs through the docker socket and granule's state
   directory today, and sandbox pods through the Kubernetes API. Whatever
   a run host is given for that is its privileged surface, and should be
   the narrowest that works. **Decided for local container sandboxes
   (task 3730): nothing, so the pair is refused.** Their `mcpserver`
   needs the docker socket, and mounting it would hand every run host
   root on the machine. Routing tool calls through the daemon would not
   survive the restart a run host exists to survive.
   `TestRunHostCannotReachAContainerSandbox` (`pkg/runhost`, in the "run
   host" CI job) makes the real call from a run host and fails if it ever
   succeeds, which is when the refusal should be lifted. The same
   question holds for the data directory, which no run host mounts: its
   `mcpserver`'s `-data-dir` names `secrets/github`, the GitHub
   credential ladder behind `pull_request_status`, `base_branch_status`,
   the GitHub-backed `wait_for_checks`, `query_merge_queue`'s
   default-branch lookup, and `list_issues`/`read_issue`. In a run host
   these read GitHub anonymously, which fails for a private repository.
   The grant directories under it (`-grain-secrets-dir`,
   `-grain-state-dir`) are absent too. `grain-log` and
   `deployment-health` then refuse rather than answer unredacted. The
   self-debug checkout (`-grain-src-dir`) is a third path that is not
   mounted.
7. **Is the front door worth its own process**, or do we accept a
   several-second gap for `git` and the model proxy? Stage 1's
   measurement is meant to answer it.
8. **Which of this is a settings change** (the adoption allowance, the
   default upgrade policy, the credential volume's storage class), and
   therefore a pull request against `grain/config` rather than against
   this repository?
