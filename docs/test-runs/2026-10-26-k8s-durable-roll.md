# Kubernetes acceptance test: a durable run across a Deployment roll (DT-S3)

**Result: both modes pass.** A durable task's run crosses a `kubectl
rollout restart` of the daemon's Deployment. With run pods it is adopted,
and without them it is suspended and resumed. Either way it opens its pull
request. This follows
[`2026-10-15-k8s-daemon-roll-adoption.md`](2026-10-15-k8s-daemon-roll-adoption.md),
which stopped before the roll on task 3858's reachability gap. That gap
has since been closed (`agent.RunConfig.K8sPod`, `grain mcpserver
-k8s-pod`).

## What ran

- **Where:** a kind cluster (`kindest/node:v1.36.1`) in a task sandbox
  with 7 vCPUs and 15 GiB, created with the local-registry recipe from
  the 2026-10-15 run.
- **The task:** the test's task holds the `durable` grant
  (`model.DurableCapability`). Its run has no deadline and takes the
  priority path.
- **The roll:** `kubectl rollout restart`, with
  `GRAIN_RUNHOST_KIND_REGISTRY` unset. See below for why the Upgrade path
  was not used.

```sh
cd pkg/cli
GRAIN_RUNHOST_KIND=grain-adopt go test -run '^TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest$' -v -timeout 40m .
GRAIN_RUNHOST_KIND=grain-adopt go test -run '^TestDaemonRollResumesADurableRunItDidNotAdopt$' -v -timeout 40m .
```

### Case 1: adopted (`-run-host-image=self`)

`--- PASS: TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest (49.41s)`

- The run pod carried no deadline label, which is right for a durable run.
- After the roll the run pod was still there, and the sandbox pod had the
  same UID.
- The next daemon pod adopted the run. The same run ID ended `succeeded`
  with its `StartedAt` unchanged.
- The transcript held each line once, in order.
- The pull request opened, and no run pod was left behind.
- The second pod logged `run-host adoption: 1 adopted, 0 lost`.

### Case 2: suspended and resumed (claude a child of the daemon pod)

This mode is what every run on kubernetes-granule gets, because that
backend has no run pods.

`--- PASS: TestDaemonRollResumesADurableRunItDidNotAdopt (53.73s)`

> run runhost-kube-roll-1 ended interrupted across the roll, suspended
> (restart); run runhost-kube-roll-2 resumed it -3s after the second daemon
> pod came up and opened the pull request

(The test notices the new pod is serving a few seconds after the pod
has started dispatching, which is why the figure is negative.)

What it checked:

- The first run was `interrupted`, with a `task_suspension` row of kind
  `restart` that names the second run as `resumed_run_id`.
- The second run started on the new pod's first cycles, within two
  minutes. A retry backoff would have held it longer.
- The stub claude's second exec was `--resume stub-session`. The stub
  refuses a resume unless the session file the first exec wrote under
  `$HOME/.claude` is still there, and `HOME=/data/home` is on the claim.
- The resumed run ended `succeeded`, and the pull request opened for the
  task's branch.

## What had to be fixed to get there

The test had drifted from the daemon since 2026-10-15:

1. **`-agent-framework` is gone.** The daemon started with "ignoring 1
   flag(s)" and "no configured agent backend is free: of 0 configured".
   The seed role now imports a claude connection.
2. **Sandbox pods need grain's PriorityClasses** ("the cluster has no
   PriorityClass named grain-agent"). The test manifest now ships the
   four classes.
3. **Shipped-manifest bug: the probes got 401.** The daemon pod sat at
   1/2 Ready with "Readiness probe failed: HTTP probe failed with
   statuscode: 401". A single-tenant daemon answers `/api/config` with
   401 to the kubelet. The `grain-sandbox` Service had no endpoints, so
   every clone failed with "Failed to connect to <cluster IP> port 9728".
   With a liveness probe on the same path, kubelet would also restart
   the daemon every few minutes. Both shipped manifests and the test now
   probe `/api/auth/access-code`. The test Service also sets
   `publishNotReadyAddresses`, as the shipped one does.
4. **Task branches follow the deployment's branch template**
   (`grain/<deployment>/task-<id>-<slug>`), so the daemon never found
   the branch the test pushed. The seed now fixes `Task.Branch`.
5. **The upgrade role needs the access code**, sent as a bearer token
   read from `/data/local-api-token`.
6. **Namespaces were stamped by the second**, so two runs started
   together shared a namespace. They are now stamped by the nanosecond.

## Not done

- **The Upgrade path.** With `GRAIN_RUNHOST_KIND_REGISTRY=localhost:5001`
  the upgrade's health check failed in 2s with "the check pod is
  Missing", leaving the Deployment as it was. So `stageKube`'s flush is
  still unexercised on a cluster.
- **kubernetes-granule itself.** kind cannot boot a packed granule guest.
  Case 2 does not depend on the sandbox backend (drain, suspension,
  priority start, `--resume`, session store on the claim), but it has not
  been run on a real `-k8s-granule-sandboxes` cluster.
