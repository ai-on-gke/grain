# Kubernetes acceptance test: a run adopted across a Deployment roll (task 3858)

> **Superseded:** the test passes as of
> [`2026-10-26-k8s-durable-roll.md`](2026-10-26-k8s-durable-roll.md).

**Result: does not pass yet. It stops before the roll, on a gap in grain
rather than in the test.** A claude run cannot start against a Kubernetes
sandbox at all. The MCP server it is handed has no way to reach a sandbox
pod (finding 1). So with `-k8s-sandboxes` and `-run-host-image`, no run
ever gets a run pod to adopt. The test is in place as the gate for that
fix. Nothing past the first dispatch has been exercised on a cluster yet:
the Upgrade's `stageKube` flush, `K8sSandboxes.Readopt`, and
`ResumeAdoptedRun` opening the pull request.

## What the test is

`TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest`
(`cmd/grain/daemon_runhost_kube_test.go`) is
`TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest` done on
Kubernetes. It runs only with `GRAIN_RUNHOST_KIND=<kind cluster>`.

- **The daemon.** The real `grain daemon` runs as
  `deploy/kubernetes/grain.yaml`'s shape of Deployment: one replica,
  Recreate, and a ReadWriteOnce claim, with the manifest's RBAC and its
  `grain` and `grain-sandbox` Services. Its flags are `-k8s-sandboxes`,
  `-run-host-image=self`, `-k8s-run-host-volume=grain-data:/data`,
  `-run-host-credential-dir=/data/agent-home` and `-run-host-daemon-url`
  pointing at the Service.
- **One image.** The daemon, the run pods and the sandbox pods all run it:
  `buildpack-deps:bookworm-scm` plus this checkout's `grain`, the
  `cmd/grain` test binary and `kubectl`. It sets the user and working
  directory that `Dockerfile.k8s-sandbox` sets. The build stamps its own
  `defaultK8sSandboxImage`, because an upgrade's checks pull whatever
  sandbox image the candidate names.
- **The test binary inside the pod** (`TestDaemonRollAdoptionRole`) has
  four roles:
  - an init container that seeds the claim once: the task, the
    `{"*": "anonymous"}` GitHub credential, and `restartStubClaude` with
    its token in the credential directory;
  - `inspect`, exec'd into the serving daemon pod, which reads the run
    rows, the transcript and `upgrade-status.json` out of the store;
  - `upgrade`, which makes the Upgrade button's `POST /api/upgrade`;
  - a `forward` sidecar (finding 2).
- **GitHub** is githubsim with a `git http-backend`, served by the test
  process on the kind network's gateway.
- **The roll.** With `GRAIN_RUNHOST_KIND_REGISTRY` set, two builds go to
  a local registry and the roll is an Upgrade to the second
  (`-upgrade-image`, branch `main`). That means `stageKube` flushes and
  the Deployment ends up pinned to a digest. Without the registry, the
  image is put on the node with `kind load` and the roll is
  `kubectl rollout restart`.
- **What it asserts after the roll:**
  - the run pod is the same pod;
  - the sandbox pod has the same UID, so it was claimed back rather than
    reaped and recreated;
  - no pull request has opened before the stub is released.

  **Once the stub is released, it asserts the docker test's checks:**
  - the same run ID ends `succeeded`;
  - `StartedAt` is unchanged;
  - the pull request is opened for the task's branch;
  - the transcript holds each line exactly once, in order;
  - no run pod is left;
  - the second pod logs `run-host adoption: 1 adopted, 0 lost`;
  - on the Upgrade path, the first pod logs
    `flushed the spool cursors of 1 run(s)`, and the Deployment runs
    `<repo>:main@sha256:…`.

## The cluster

kind v0.32.0 (node v1.36.1, containerd 2.3.1) in a task sandbox: 4 vCPUs,
7 GiB of memory. This used kind's local-registry recipe:

```sh
docker run -d --restart=always -p 127.0.0.1:5001:5000 --name kind-registry registry:2
cat <<YAML | kind create cluster --name grain-adopt --wait 3m --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
YAML
for node in $(kind get nodes --name grain-adopt); do
  docker exec "$node" mkdir -p /etc/containerd/certs.d/localhost:5001
  printf '[host."http://kind-registry:5000"]\n' |
    docker exec -i "$node" cp /dev/stdin /etc/containerd/certs.d/localhost:5001/hosts.toml
done
docker network connect kind kind-registry

cd cmd/grain
GRAIN_RUNHOST_KIND=grain-adopt GRAIN_RUNHOST_KIND_REGISTRY=localhost:5001 \
  go test -run '^TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest$' -v -timeout 40m .
```

kind could create a cluster in this task sandbox. grain CI's own runners
are the ones that cannot (`.grain/workflows/tests.yml`).

## The run

The run used the Upgrade path. It got through image builds and pushes,
the first daemon pod starting, the dispatch, the sandbox pod and the clone
through the git proxy, which reached githubsim. Then it failed:

```
orchestrator: run runhost-kube-roll-1 (task runhost-kube-roll): orchestrator: running runhost-kube-roll-1: claude: RunConfig.SandboxRoot, .GranuleVM, or .Container is required
orchestrator: run runhost-kube-roll-2 (task runhost-kube-roll): ... the same
orchestrator: claude: usage limit reached; 3 runs failed within 5m0s of each other, each in under 1m0s and before any tool call, all with the same error -- read as the provider refusing claude: claude: RunConfig.SandboxRoot, .GranuleVM, or .Container is required -- dispatch onto connection default-claude (team default) paused until 2026-10-15T08:46:20Z
--- FAIL: TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest (378.43s)
    daemon_runhost_kube_test.go:286: the first daemon pod never had the run in a run pod
```

## Findings

1. **A claude run cannot reach a Kubernetes sandbox. This is the
   blocker.** `orchestrator.k8sSandbox` implements none of `Root`,
   `VMName` or `ContainerName`. As a result, `RunDispatch` hands claude an
   `agent.RunConfig` with no sandbox target, and `Framework.mcpServerArgs`
   refuses it. `grain mcpserver` has no flag for a sandbox pod either: it
   has only `-sandbox-root`, `-container` and `-granule-vm`.
   - It fails in claude's own arguments, so it is not specific to run
     hosts: a claude run that is a child of the daemon fails the same
     way. codex and antigravity have the same check.
   - It contradicts what `daemon.CheckRunHostBackend` says: "Kubernetes
     ones [are reached] through the API, so neither is refused".
   - `TestKubeRunHostSurvivesADaemonRoll` could not find this, because
     its stand-in daemon runs `/bin/sh` and no framework.
   - The fix: a pod target for the MCP server (`mcp.KubectlExecRunner` and
     `NewSSHSandboxTools`, already used for the daemon's own tools), a
     field on `RunConfig`, and a case in each framework. The run pod
     already runs under the daemon's service account, so that it can exec
     into sandbox pods. Filed as a follow-up. This test is what shows it
     works.
2. **The git proxy forwards over plain HTTP only to loopback.** Any mock
   GitHub has to be in the daemon pod's network namespace. The test runs a
   `forward` sidecar on `127.0.0.1:7780` that relays to the test
   process. This is a test arrangement, not a grain change.
3. **A Kubernetes sandbox image has to start `kubectl exec` in `$HOME`.**
   `mcp.ConfigureGitCredentialsOverSSH` writes `.git-credentials` and
   `.gitconfig` by relative path, so they land wherever the exec starts.
   - With an image that has no `WORKDIR`, that was `/`, while git read
     `/root`. The clone failed with
     `could not read Username for 'http://<service>:9728'`.
   - The shipped `Dockerfile.k8s-sandbox` sets `WORKDIR /home/agent` as
     `USER agent`, so it is not affected, but a deployment's own sandbox
     image could be.
   - The test image now copies that user and working directory.
   - The failure was hard to read from the run row
     (`cloning acme/widgets into work: exit=128`); only the daemon log
     had git's stderr.
4. **The configuration error in (1) was read as a provider refusal.** It
   failed three runs within a minute each, before any tool call.
   `unrecognised_refusal_burst` then paused the team's claude connection
   for 15 minutes as "usage limit reached". A run that fails in grain's own
   argument building never reached the provider, and should not count
   towards that burst.
5. **The default sandbox shape (8 GiB) does not fit a 7 GiB kind node**
   beside the daemon pod. The pod waits `Unschedulable` for two minutes,
   and then the run fails. The test's task asks for 1 CPU, 1 GiB and
   5 GB.

## Not covered

- The `kubectl rollout restart` path (no registry) was not run. It
  stops at the same point, before any roll.
- Nothing after the first dispatch: the flush, `Readopt` on a real
  sandbox pod, `ResumeAdoptedRun` and the pull request. The test asserts
  all of it, so a pass after finding 1 is fixed is the result this record
  is still waiting for.
