# Run hosts and sandboxes on Kubernetes, on kind, with the real daemon (task 3952)

This report covers §9 of `docs/test-plans/2026-10-15.md`, "Run Hosts and Sandboxes on Kubernetes;
Granule Readoption With Address Pools". It was filed by the nightly test-plan task 3749, and the
base is `candidate` at `591f0f35f`.

The 10-13 walk (`2026-10-13-k8s-run-host-roll.md`) used a stand-in daemon, a shell-loop agent and no
sandbox. This walk used the real `grain daemon` from this tree, applied from `deploy/kubernetes/grain.yaml`
with run hosts and `-k8s-sandbox-service` on. It ran a real task whose agent pushed from a real sandbox pod
through the `grain-sandbox` Service, across a `kubectl rollout restart`. It also exercised the
Kubernetes upgrade path against a registry.

**Summary:**
- **Before any fix:** no task could run at all. Every claude run on `-k8s-sandboxes` failed at
  dispatch (F1). After F1 was fixed, the run pod's MCP server was refused by the daemon API (F4,
  still open).
- **With F1–F3 and F5 fixed and the F4 workaround in place:** steps 3, 4, 5 and 6 pass. The
  workaround is `-ui-trusted-cidrs`.

## Commits under test

`git branch -r --contains` lists `origin/candidate` and `origin/main` for each of these: `418da4a6a`,
`5a9bdf25b`, `15f93c490`, `4aa6a58b7`, `7c5386a0d`, `0410d57ce` and `698fd097f`.

## Environment

- kind v0.32.0 go1.26.3 linux/amd64, `kind create cluster --name g9` in the task sandbox.
- Node image `kindest/node:v1.36.1@sha256:3489c767…`, one node, server v1.36.1. The storage class is
  `standard` (rancher local-path, ReadWriteOnce). The kind network gateway is `10.241.0.1`.
- **Sandbox image:** `grain-k8s-sandbox:walk`, built from `Dockerfile.k8s-sandbox`
  with `INSTALL_CLAUDE_CLI=0 INSTALL_AGY_CLI=0 INSTALL_CODEX_CLI=0`. It is the real sandbox image
  without the agent CLIs: Debian 12.15, git 2.39.5, bash, curl, tesseract and so on. `.grain-ca.crt`
  was staged for the build and removed afterwards.
- **Daemon image (slim, not the full `Dockerfile`):** `FROM grain-k8s-sandbox:walk` plus:
  - `/usr/local/bin/grain`: this tree, `CGO_ENABLED=0`.
  - `/usr/local/bin/kubectl`: the sandbox host's own binary.
  - `/usr/local/bin/claude`: the stub, below.
  - `/usr/local/bin/tcpfwd`: a 30-line TCP forwarder, below.

  Compared with the full `Dockerfile` it lacks tini, cosign, the docker CLI, granule, openssh-client,
  procps, systemd/journalctl and the real claude/agy/codex CLIs. The entrypoint is `grain` itself.
  Two `apt-get`/`RUN` layers on top of the sandbox image failed with exit 2 for reasons I did not
  chase, so nothing was apt-installed into it.
- **Image tags:** `grain-walk:v1` to `v5` were loaded with `kind load`. For the upgrade step,
  `kind-registry:5000/grain-walk:v1.0.0` (v4's grain) and `:v1.0.1` (v5's grain) were pushed to a
  local `registry:2` on the kind network. Each carries a distinct marker file, so both have
  registry digests the node never saw through `kind load`.

### Manifests applied

`deploy/kubernetes/grain.yaml`, with these walk-only edits:
- **Images:** `grain-walk:vN` (later `kind-registry:5000/grain-walk:v1.0.0`),
  `-k8s-sandbox-image=grain-k8s-sandbox:walk`, `-upgrade-image=` that repository, no
  `-upgrade-verify-cmd` (no cosign in the image).
- **Run hosts on:** the five commented `-run-host-*` / `-k8s-run-host-volume=grain-data:/data` /
  `-run-host-daemon-url=http://grain.grain.svc` lines are uncommented. `-k8s-sandbox-service=grain-sandbox`
  is unchanged from the manifest.
- **Stand-in GitHub and stub agent:** `-default-target-repo=acme/widgets`,
  `-github-host=127.0.0.1:8099 -github-insecure-http`, `-claude-path=/usr/local/bin/claude`,
  `-claude-oauth-token-file=/data/agent-home/token`.
- **Smaller requests:** the daemon requests 100m CPU and 256Mi memory instead of 1 CPU and 2Gi.
- **Init container:** also writes `/data/agent-home/token` and
  `/data/secrets/github/credentials.json` (`{"*": "anonymous"}`).
- **`ghfwd` sidecar:** runs `tcpfwd 127.0.0.1:8099 10.241.0.1:8099`. The git proxy forwards plain
  HTTP only to a loopback address, so the stand-in GitHub has to appear on loopback inside the pod.
- **F4 workaround (from the second task on):** `-ui-trusted-cidrs=10.244.0.0/16`.
- **F2 fix:** `publishNotReadyAddresses: true` on `grain-sandbox`, before it landed in the tree.

`networkpolicy.yaml` and `runhost-rwx.yaml` were not applied: kindnet does not enforce policy, and the
claim here is ReadWriteOnce.

Team setup went through the daemon's own API from inside the pod (loopback, so trusted):
- `PUT /api/teams/default` set `maxWorkers: 1` and `agentFramework: claude`.
- `PUT /api/teams/default/agent-backends/stubclaude` added a claude connection with
  `cliPath /usr/local/bin/claude`, and its credential was set to `sk-ant-oat01-stub`.
- Tasks were filed with `POST /api/tasks` and approved with `POST /api/tasks/{id}/approve`.

## Stubs

- **The model provider (stub claude CLI):** a small Go program, kept out of the tree. It refuses to
  start, as the real CLI does, if `--mcp-config` is unreadable or HOME is not writable. It writes one
  stream-json line, then waits for `/data/agent-home/release` on the claim. Then it starts the real
  `grain mcpserver` from `--mcp-config`, speaks MCP to it over stdio, and calls `run_command` once:
  `cd work; git remote -v; …; git commit; git push origin HEAD`. The tool result goes into its
  transcript.
- **The forge:** `githubsim.Sim` for `acme/widgets`, plus `git http-backend` over a bare repo, on
  `10.241.0.1:8099` in the task sandbox. It reports a passing `build` check for any ref, and is
  reached through the `ghfwd` sidecar.
- **Registries:** `kind load` for the walk images; a local `registry:2` (`kind-registry:5000`, http,
  via `/etc/containerd/certs.d`) for the upgrade images. `ghcr.io/bwsalmon/grain/k8s-sandbox:latest`
  is the image a non-release build tells the upgrade's sandbox-image step to check; it was satisfied
  by `kind load`ing the walk sandbox image under that name.

Nothing of grain's own was stubbed: the store, dispatch, the run-host driver, the sandbox backend,
the git proxy and the upgrader are all the real ones.

## Steps and outcomes

### 1–2. Cluster, images, manifests: done

The daemon resolved the Service at startup: `sandboxes reach the git proxy, claude proxy, openai proxy,
gemini proxy at 10.96.243.120 (-k8s-sandbox-service grain-sandbox), which outlives this pod`, and
`the git proxy is serving at http://10.96.243.120:9728 (bound [::]:9728)`.

### 3. File a task; run pod, sandbox pod, git remote: pass after F1

- **First attempts (F1):** runs 2-1 to 2-3 of task 2 failed before the agent started, with
  `claude: RunConfig.SandboxRoot, .GranuleVM, or .Container is required`. This is fixed on this
  branch, and the image was rebuilt as v2.
- **Run 2-5 then started:**
  - run pod `f1f4e3f4…-runhost-run-2-5-1934250188`
  - sandbox pod `grain-sandbox-2-5`
- **Run pod labels:** `app.kubernetes.io/component=runhost`, `app.kubernetes.io/managed-by=grain`, and
  `dev.grain.runhost.{deadline=1792079288, deployment, generation=…_dirty, protocol=1, run=2-5, task=2}`.
- **Annotations:** the exact values. The deadline is `2026-10-15T15:48:08Z`, the generation keeps its
  `+dirty`, and there is `dev.grain.runhost.spool=/data/runhosts/spool/run-2-5-1934250188`.
- **Affinity:** `requiredDuringSchedulingIgnoredDuringExecution` podAffinity to
  `app.kubernetes.io/{name=grain,component=daemon}` on `kubernetes.io/hostname` (ReadWriteOnce, no
  `-k8s-run-host-rwx`).
- **Pod spec:**
  - Mounts are subPaths of `grain-data` only: `runhosts/spool`, `runhosts/staging`, `agent-home`.
  - The service account is `grain` and the grace period is 30s.
  - The command is `grain runhost exec -spool … -deadline …`.
  - The image is the daemon's own (`grain-walk:v2`). A kind-loaded image has no registry digest to
    pin, so `self` read back the tag.
- **Sandbox git remote:** `origin http://10.96.243.120:9728/acme/widgets.git`, which is the Service IP.

### 4. `kubectl rollout restart deploy/grain` mid-run: pass after F1, F2 and the F4 workaround

- **Task 2:** the run pod and sandbox pod survived the roll, and the new daemon pod logged
  `adopted run host … for run 2-5 at cursor 237 after a 2s gap` and `resuming adopted run 2-5 … 2s of
  restart credited`. The stub's MCP server then exited before answering: its `grain mcpserver` was
  refused by `http://grain.grain.svc` with 401 (F4).
- **Task 3** (with `-ui-trusted-cidrs`): the adopted run's MCP call reached the sandbox pod over
  `kubectl exec`, but the stub ran git in `/home/agent` instead of `/home/agent/work`. That was a
  stub bug, fixed in v3.
- **Task 4** (v3), the run that passed end to end:
  - At 12:39:15, `kubectl rollout restart` replaced `grain-5689495c57-7tngl` with
    `grain-dcf7846-qdfnp`.
  - The run pod (uid `8af38acb…`) and the sandbox pod (uid `69450327…`) kept their uids across the
    roll.
  - At 12:39:17 the new pod logged `adopted run host …-run-4-1-3350837522 for run 4-1 at cursor 237
    after a 2s gap` and `run-host adoption: 1 adopted, 0 lost, 0 unclaimed run host(s) freed`.
  - After the stub was released, its `run_command` in the sandbox pod printed the Service-IP remote
    and `* [new branch] HEAD -> grain/f1f4e3f4/task-4`, `PUSH_EXIT=0`.
  - The daemon then `POST /repos/acme/widgets/pulls -> 201`: pull request acme/widgets#9000 opened,
    and the task moved to `awaiting_submit`.
  - The run pod and `grain-sandbox-4-1` were both gone when the run ended. Only the daemon pod was
    left.
- **The transcript has both halves:** "stub started in …-run-4-1… before the roll", then "released
  after the roll", then the tool result.

### 5. A Service that never resolves: pass after F3

- **Before the fix:** with `-k8s-sandbox-service=grain-nosuch`, the daemon went silent after its
  startup lines. It never logged an error, never exited and never served
  (readiness 1/2), until the liveness probe would kill it minutes later (F3).
- **After the fix (v4):** the pod started at about 12:45:02 and exited at about 12:45:32. The next
  container logged `grain daemon: resolving -k8s-sandbox-service grain-nosuch.grain.svc: lookup
  grain-nosuch.grain.svc on 10.96.0.10:53: no such host`, and the pod showed `Error` then restarted.
- **Against the plan:** the daemon refuses to start after about 30s, as planned, but it does not log
  a line per retry. It retries silently every 2s and logs only the final error.

### 6. The Kubernetes upgrade: pass

Each upgrade was started with `POST /api/upgrade {"branch": …}`. The branch must be in
`-upgrade-allowed-branches`, so `v1.0.0` and `v1.0.1` were used.

- **A, to `v1.0.0` (the tag already running, unpinned).** This pinned the deployment to
  `kind-registry:5000/grain-walk:v1.0.0@sha256:1d2bd988…` and rolled out.
- **B, to `v1.0.0` again (the same pinned image).** The log read `not flushing the runs in run hosts:
  deployment/grain already runs kind-registry:5000/grain-walk:v1.0.0@sha256:1d2bd988…, so nothing
  rolls out`, then `roll out: done in 38ms`. No new pod appeared and the generation was unchanged
  (F6 is about what it said afterwards).
- **C, to `v1.0.1` (a new tag).** The log read `upgrade: flushed the spool cursors of 0 run(s) in run
  hosts before the restart: []` at 13:04:59, *before* `step 6/6 roll out: started` at 13:05:03, and
  the new pod came up at 13:05:05. No run was in flight at that moment: the upgrade hold kept the
  queued check-upgrade task from dispatching, and I did not file another run first. So the ordering
  is shown, but the flush of a live run's cursor across this path is not; task 4's roll above covers
  adoption.
- **Earlier failures, both environmental:**
  - The health check failed against kind-loaded images: `reported no registry digest … (its imageID
    was "docker.io/library/import-2026-10-15@sha256:…")`. This is correct behaviour, and the reason
    for the registry. Re-pulling the same content from the registry kept the import digest, so
    distinct images were pushed.
  - The sandbox-image step pulled `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` until it was loaded.
    It is bounded at 10m.

### 7. `docs/non-disruptive-upgrade.md` §Stage 2

Contradicted by this run, and corrected on this branch:
- **"Reaching the daemon":** the doc said a run pod's `mcpserver` "execs into sandbox pods just as
  it did as the daemon's child". No such transport existed (F1). The Service also refuses the run
  pod's API calls (F4).
- **"Still missing before a real task finishes its pull request across a roll", items 1 and 2:**
  the post-processing resume and sandbox reaping by claim are done. The walk finished a pull request
  across a roll, and the sandbox pod was claimed rather than reaped.

## Findings

### F1: no CLI-framework run could start on `-k8s-sandboxes` (fixed)

**Reproduction:** run any `-k8s-sandboxes` daemon with a claude, codex or antigravity connection,
with or without run hosts, and file a task. The run fails in under a second with `claude:
RunConfig.SandboxRoot, .GranuleVM, or .Container is required`. Three of these in 5 minutes are then
read as a usage limit, and the connection is paused for 15 minutes.

**Cause:** `k8sSandbox` answers none of `rootedSandbox`, `vmNamedSandbox` or `containerNamedSandbox`,
and `grain mcpserver` had no transport for a pod. The 10-04 walk used the `custom` framework, and the
10-13 walk used no MCP at all, so neither could hit this.

**Fix:** `fix(k8s): let claude, codex and antigravity runs reach a sandbox pod`.
- `k8sSandbox.KubePod()` (`podNamedSandbox`) is carried on the run's Config into
  `agent.RunConfig.K8sPod`.
- All three frameworks pass `mcpserver -k8s-pod P -k8s-namespace N -workspace W`.
- mcpserver serves the sandbox tools over the existing `mcp.KubectlExecRunner`.

**Tests:**
- `TestMCPServerArgsReachAKubernetesSandboxPod` (pkg/agent/claude)
- `TestMCPServerK8sPodTransportFlags` (pkg/daemon)
- `TestK8sSandboxNamesItsPodToForkedMCPServers` (pkg/orchestrator)
- Verified live by step 4.

**Not covered:** `-k8s-granule-sandboxes` has the same gap. Its tools wrap `KubectlExecRunner` in
`granuleExecRunner`, which this transport does not reproduce. It was proposed as a separate task.

### F2: the first dispatch after a daemon restart can fail its clone (fixed in the manifest)

**Reproduction:** with run 2-3 backing off, `kubectl set image` the daemon. The new pod dispatched
run 2-4 at once, and its clone failed: `Failed to connect to 10.96.243.120 port 9728 after 0 ms:
Couldn't connect to server`.

**Cause:** the daemon dispatches before its readinessProbe passes (5s initial delay, 10s period). Until
it does, the `grain-sandbox` Service has no endpoint.

**Fix:** `publishNotReadyAddresses: true` on `grain-sandbox`. This is safe because the proxies listen
before anything is dispatched, and `Recreate` never leaves a second daemon pod. With the fix, the
EndpointSlice listed the new pod as ready while it was still `PodInitializing`.

**Test:** `TestGrainSandboxServicePublishesTheDaemonBeforeItIsReady` (pkg/hostinit). It parses
`deploy/kubernetes/grain.yaml`, and fails without the change.

### F3: a startup error after the state sync starts hangs the daemon (fixed)

**Reproduction:** step 5 before the fix. A SIGQUIT goroutine dump showed goroutine 1 in
`daemon.Run.func5` at `run.go:174`, blocked on `<-syncStopped`.

**Cause:** `Run` defers a wait for `stateSyncLoop`, which stopped only when the signal context was
cancelled. An error return leaves that context live, so `log.Fatalf` was never reached. Any
startup error after the loop begins is affected, not only the Service lookup.

**Fix:** `fix(daemon): exit on a startup error instead of hanging in the state sync`. `startStateSync`
gives the loop its own context, and its stop cancels that context and then waits, so the final export
still runs.

**Test:** `TestStateSyncStopsWithoutItsParentContextEnding` (pkg/daemon). Verified live by step 5.

### F4: a run pod's mcpserver is refused by the daemon API (open; task proposed)

**Reproduction:** with run hosts on Kubernetes and `-run-host-daemon-url=http://grain.grain.svc` as
the manifest says, the run pod's MCP server exits at startup. In a pod with the same mounts:
`grain mcpserver … -server http://grain.grain.svc -task 2` →
`grain mcpserver: fetching task 2: grain server: sign in to use this server's API (401 Unauthorized)`.

**Effect:** on Kubernetes with run hosts, every claude run loses its MCP server. It has no tools, and
so it cannot push.

**Cause:** the API trusts loopback, in-process hops and `-ui-trusted-cidrs`. Only remote executors
get per-run grants (`modelexec.RemoteHost.AuthorizeMCPArgs`), and run pods get nothing.

**Workaround used here:** `-ui-trusted-cidrs=10.244.0.0/16`. This also trusts every sandbox pod, so
it is not something to recommend.

**Why not fixed here:** the fix is a per-run credential for run-pod mcpservers. That is an
authorization design change, so it was proposed as its own task rather than made here.

### F5: the Kubernetes upgrade startup line was wrong (fixed)

**What it said:** `grain daemon: upgrades pull grain-walk and rewrite ; nothing restarts afterwards
(no -upgrade-restart-cmd)`. The ref file is empty, and on Kubernetes the rollout is the restart.

**Fix:** `fix(daemon): say what a Kubernetes upgrade repoints at startup`. The line now reads
`upgrades health-check kind-registry:5000/grain-walk:<branch> in a throwaway pod and repoint
container grain of deployment/grain at it; the rollout restarts this pod`, as seen live on v1.0.1.

**Test:** `TestUpgradeImageNoteNamesTheDeploymentOnKubernetes`.

### F6: a no-op Kubernetes upgrade still says "restarting" and files a check task (open; task proposed)

**Reproduction:** step 6 B. The result was `rolled out …@sha256:1d2bd988… …; restarting`, and
`orchestrator: filed task 6 to check the upgrade to v1.0.0 (auto)`, yet no pod restarted.

### Notes that are not bugs

- **Usage-limit reading of config errors:** F1's configuration error was read as a provider usage
  limit and paused the connection. That is the existing heuristic: three sub-minute failures before
  any tool call. It made F1 look like a quota problem in the task pane.
- **Run pods run a tag on kind:** a kind-loaded image has no registry digest, so `-run-host-image=self`
  runs the tag. With the registry images after upgrade A, the Deployment held the pinned digest.
- **Collateral cleanup on the node:** while chasing the digest, I removed `import-*` image
  references on the node, including kind's own preloaded ones. Nothing restarted afterwards, so it
  did not matter here.

## Not run

- **Granule readoption with address pools (`698fd097f`):** it needs a granule host, so it is a
  unit-tested claim only (`sandboxaddr` `ReserveRoot` tests and the orchestrator readopt tests).
  K8s-granule sandboxes were not walked either, and see F1 for why claude cannot reach them.
- **Other Kubernetes setups:** RWX run hosts (`-k8s-run-host-rwx`), a multi-node reschedule, and
  NetworkPolicy enforcement (kindnet does not enforce it).
- **Upgrade C with a run in flight:** the flush ordering is shown, but not a live run's cursor being
  flushed and then adopted on this path.
- **Real images and CLIs:** the full `Dockerfile` image, cosign verification, and the real claude CLI.

## Fixes on this branch

| Commit | Finding | Regression test |
| --- | --- | --- |
| `fix(k8s): let claude, codex and antigravity runs reach a sandbox pod` | F1 | 3 Go tests + live step 4 |
| `fix(k8s): publish the daemon on grain-sandbox before it is ready` | F2 | manifest test |
| `fix(daemon): exit on a startup error instead of hanging in the state sync` | F3 | Go test + live step 5 |
| `fix(daemon): say what a Kubernetes upgrade repoints at startup` | F5 | Go test + live log |

`docs/non-disruptive-upgrade.md` §Stage 2 is corrected as in step 7.

## Grain CI

See the closing note on task 3952 for the full-suite verdict on the final commit.
