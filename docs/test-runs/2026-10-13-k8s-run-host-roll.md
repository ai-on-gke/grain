# A run pod survives a daemon roll on kind (task 2991)

**What it checks:** the Kubernetes half of Stage 2's acceptance test,
from the run host's side (design/design/non-disruptive-upgrade.design.md). A run is
started in a run pod. The daemon Deployment is rolled mid-run. The run
pod keeps running with no daemon, and the next daemon pod adopts it.

**How:** `GRAIN_RUNHOST_KIND=rh go test -run TestKubeRunHostSurvivesADaemonRoll -v ./pkg/runhost/`,
in a task sandbox, against `kind create cluster --name rh`.

- kind: kind v0.32.0 go1.26.3 linux/amd64
- node image: kindest/node:v1.36.1, one node, storage class `standard`
  (rancher local-path, ReadWriteOnce)

The stand-in daemon is the test binary itself
(`TestKubeRunHostDaemonRole`), in an image with grain and kubectl,
loaded with `kind load`. It runs in grain.yaml's shape: a one-replica
`Recreate` Deployment with `app.kubernetes.io/component: daemon`,
uid 1000, a ReadWriteOnce `grain-data` claim at /data, a namespaced
Role, and the preferred podAffinity towards run pods. It drives a real
`runhost.Host` with the `runhost.Kubernetes` driver: subPath mounts
of /data/runhosts/{spool,staging}, a required podAffinity to the
daemon's node, and `grain runhost exec` as the entrypoint. The "agent"
is `/bin/sh` printing 120 lines, 0.25s apart. A JSON file on the claim
stands in for the store's spool cursor.

## What was stubbed

- The agent CLI (a shell loop) and the store (the cursor file). No task,
  pull request or model is involved, because adopting a run's
  post-processing is not built yet (see the design doc's Stage 2 note).

## Result

```
=== RUN   TestKubeRunHostSurvivesADaemonRoll
    kube_integration_test.go:141: daemon pod grain-69696d5cc8-wx7hv started run pod kubetest-runhost-run-r1-669169100 on node rh-control-plane; rolling the daemon
    kube_integration_test.go:156: second daemon pod grain-754d77f7d6-4z6sr:
        === RUN   TestKubeRunHostDaemonRole
        ADOPTING kubetest-runhost-run-r1-669169100 at cursor 49 after a 895ms gap
        ADOPTED-OK: 120 lines, 7 read by the first daemon and 113 by this one
--- PASS: TestKubeRunHostSurvivesADaemonRoll (50.44s)
PASS
ok  	github.com/ai-on-gke/grain/pkg/runhost	50.441s
```

- The first daemon pod started run pod
  `kubetest-runhost-run-r1-669169100`. After the roll, the second pod
  adopted that same pod: there was one run pod, and nothing recreated it.
- The second daemon pod adopted it at cursor 49, after a gap of 895ms
  between the old pod's last persisted cursor and the adoption. The
  first daemon had tee'd 7 lines and the second tee'd 113. Together they
  are the whole stdout, byte for byte.
- After the run, the run pod was deleted and its spool removed.

Two earlier attempts failed, and both were faults in the test, not the
driver. A placeholder collided with an environment variable's name, and
the test treated a terminating pod (`Destroy` does not wait) as one left
behind.

## Not covered here

- A real `grain daemon` and a real claude run finishing its pull request
  across the roll. That needs the post-processing resume, claim-based
  sandbox reaping, and a stable git-proxy address, which are listed in
  the design doc as still missing.
- The case where the daemon pod is rescheduled to a different node. kind
  has one node here. The behaviour is in grain.yaml's comment on the
  Deployment's affinity.
