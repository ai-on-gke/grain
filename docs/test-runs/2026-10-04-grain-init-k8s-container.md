# 2026-10-04: `grain init -platform=k8s-container` on a kind cluster

This run executes §10 of `docs/test-plans/2026-10-04.md` (task 1836, base `main`).

## Environment

- Sandbox: Debian, kernel 6.1.0-53-amd64, 4 vCPU, 7.8 GB RAM, `/dev/kvm` present (unused here).
- Docker 29.8.2 (server).
- `kind` v0.32.0 (go1.26.3). The cluster `grain` is one control-plane node running Kubernetes
  **v1.36.1** on containerd v2.3.1, with kindnet CNI and the `standard` local-path StorageClass
  (`WaitForFirstConsumer`).
- `kubectl` client **v1.35.8-dispatcher** (`/usr/bin/kubectl`, from the Google Cloud SDK). It was
  already in the sandbox image, as was `kind`.
- Go 1.26.6. Node 20.19.0. Playwright 1.62.1 (chromium from `/var/cache/playwright`).

**Tested commit:** `9715b22e1` (`main`), plus this branch's fixes. The three §10 commits are all
in it: `git branch -r --contains` lists `origin/main` (and not `origin/candidate`) for
`0a87631ee`, `b94191fda` and `8b6d101a0`.

### What the cluster ran

- **Steps 1 to 5 (preflight, first full run, idempotent re-run):** the published
  `ghcr.io/bwsalmon/grain/grain:main` image that `deploy/kubernetes/grain.yaml` names. Its
  `org.opencontainers.image.revision` is `18c7c6d2b`, which contains `0a87631ee` but is about 14h
  behind the tested commit.
- **Sign-in and dispatch (later sections):** the deployment's image was swapped with
  `kubectl set image` for `ghcr.io/bwsalmon/grain/grain:walkthrough2`. That image is the published
  one with `/usr/local/bin/grain` replaced by a `CGO_ENABLED=0` build of this branch, including the
  production UI bundle (`cd ui && npm ci && npm run build`). It was loaded into kind with
  `kind load docker-image`.
- `grain init` itself was always `/tmp/grain`, built with `go build -o /tmp/grain ./cmd/grain`
  from the tested commit. `/tmp/grain-fixed` is the same build with this branch's fixes.

### Environment fix (not a grain bug)

At first, the kind node could not pull from ghcr.io: `x509: certificate signed by unknown
authority`. The sandbox's package cache intercepts registries with its own CA, and the kind node
did not trust it. The fix was to copy `/usr/local/share/ca-certificates/grain-ca.crt` into the
node, then run `update-ca-certificates` and `systemctl restart containerd`.

## Commands and exit codes

Every `grain init` below ran from the checkout as
`-platform=k8s-container -repo-root=$PWD -data-dir=/tmp/k8s-init` plus the flags shown.
The token flags `T` are `-github-token-file=/tmp/k8s-init-token -github-credential-name=walkthrough -github-repo=grain-walkthrough/demo`.

| # | Command | Exit | Result |
|---|---|---|---|
| 1 | `/tmp/grain init -platform=k8s-container -data-dir=…` (no `-repo-root`) | **1** | `-repo-root is required`. This is required even though k8s-container only uses it to find the manifests. |
| 2 | no kubeconfig at all (`~/.kube` absent) | **10** | `[refused] kubectl cluster access`: "grain init needs a working kubectl already pointed at the target cluster…". Five repeated `memcache.go` lines come first, because kubectl falls back to `localhost:8080`. |
| 3 | `-k8s-context=kind-nope` | **10** | `[refused] kubectl cluster access … error: context "kind-nope" does not exist`. |
| 4 | `-k8s-kubeconfig=/tmp/does-not-exist` | **10** | `[refused] … stat /tmp/does-not-exist: no such file or directory`. |
| 5 | `-github-token-file=… -github-repo=…` without `-github-credential-name` | **1** | `hostinit: GitHubCredentialName is required with GitHubToken`, in Go field names (**F1**). |
| 6 | `T`, first run, node not trusting the cache CA | **10** | `[manual] sandbox image pulls (ImagePullBackOff)`. `[manual] storage class offers ReadWriteOnce` was wrong (**F2**). 3m03s. |
| 7 | `-k8s-sandbox-image=registry.invalid/nope:1`, separate data dir | **10** | Deterministic reproduction of **F2**: the PVC is `Bound`, yet the step says the class "needs a class that actually gives ReadWriteOnce volumes". |
| 8 | `T`, full run | **0** | Every preflight check `[skipped]` (passed). Then `[done]` for: apply the grain manifest, wait for the rollout, reach the daemon's API (its own port-forward), confirm the API is reachable, provision the admin password (**for `debian`**: **F3**), target repos `grain-walkthrough/demo`, `maxWorkers=1`. The sandbox budget step was `[skipped]`. There was **no GitHub credential step at all** (**F4**). 1m29s. |
| 9 | `T`, re-run | **0** | Nothing recreated (see below). `apply the grain manifest [skipped]: deployment/grain already exists…`, admin, repos and workers all `[skipped]`. **1m12s**, mostly scratch-pod termination (**F5**). |
| 10 | `/tmp/grain-fixed`, `T` | **0** | `provision the admin account's password [done] created "grain"'s password`. 16s. |
| 11 | `/tmp/grain-fixed`, `T` | **0** | 14s. `"grain" already has an account`. It also deleted `admin-password`: candidate's F4 (see **F6**). |
| 12 | `/tmp/grain-fixed`, `T` (after the credential fix) | **0** | `[done] give the default team the GitHub token: stored as the default team's own GitHub credential`. |
| 13 | as 12 | **0** | `[skipped] … the default team already has one (grain-team-ZGVmYXVsdA)`. |
| 14 | `/tmp/grain-fixed`, view-only identity (`-k8s-kubeconfig=/tmp/limited.kubeconfig -k8s-context=limited`; SA `default:limited` bound to ClusterRole `view`) | **10** | `[manual] permission to create what grain.yaml creates`: "cannot create: namespaces, serviceaccounts, roles…, rolebindings…, persistentvolumeclaims, configmaps, deployments.apps, services". `[refused] sandbox image pulls (Forbidden)`. The "Run these" list printed two **blank lines** (**F7**). |

The exit codes match `docs/grain-init.md` "The three exit codes" and the `linux-*` platforms: 0 for
up, 10 for steps remaining, 1 for bad flags.

### Objects after run 8, and idempotency (run 9)

- `kubectl get ns grain --show-labels`: `pod-security.kubernetes.io/enforce=baseline`,
  `audit=restricted`, `warn=restricted`.
- `kubectl -n grain get all,pvc`:
  - `deployment.apps/grain` 1/1, `generation 1`, image `ghcr.io/bwsalmon/grain/grain:main`;
  - `service/grain` ClusterIP, port 80/TCP;
  - `persistentvolumeclaim/grain-data` Bound, 20Gi RWO, class `standard`;
  - one pod, `grain-7f45867465-b46lc`, Running as `uid=1000(grain)`.
- Before and after run 9, a snapshot of `uid`, `generation` and `resourceVersion` for the
  Namespace, Deployment, Service and PVC, plus the pod name, was **identical** (`diff` empty).

## Sign-in (Playwright)

This used the `walkthrough2` image, `kubectl -n grain port-forward svc/grain 18420:80`, and
chromium with `extraHTTPHeaders: {"X-Forwarded-For": "203.0.113.5"}`. The header makes the browser
a remote person. Without it, a port-forwarded request reaches the pod from 127.0.0.1 and is
trusted as the daemon (`isTrustedSource`). The script is `/tmp/pw/signin.mjs`. It clicks the
dialog's **Log in** tab, fills Username and Password, and submits.

- Anonymous `GET /api/teams`: **401**.
- Sign in as `grain` with a wrong password: "invalid username or password".
- Sign in as `grain` with the provisioned password: the dialog closes. `/api/config` returns
  `actor: grain, authenticated: true, isAdmin: true`. `/api/teams` returns 200, with the default
  team at `maxWorkers: 1` and `targetRepos: ["grain-walkthrough/demo"]`. `/api/secrets` and
  `/api/sandboxes` return 200.
- On the original image with the original default (run 8), the provisioned `debian` account
  signed in, but `isAdmin: false` and `/api/secrets` returned **403** (**F3**).
- The password file had been deleted by run 11 (F6), so `grain`'s password was reset over the
  trusted loopback API (`PUT /api/users/grain/password`) before this sign-in.

## Trivial task to a sandbox pod

1. Connected a fake custom agent: `PUT /api/settings` with
   `agentBackends: [{id: walkthrough-fake, framework: custom, command: /data/fake-agent, models: [fake-model], team: default}]`.
   `/data/fake-agent` was copied into the pod with `kubectl cp`.
2. Filed the task with `POST /api/tasks` (repo `grain-walkthrough/demo`, model `fake-model`).
   - Before run 12, this was refused: `team "default" has no GitHub credential, so it cannot
     reach grain-walkthrough/demo` (**F4**).
   - After run 12, task 2 was created (`agentFramework: custom`).
3. Approved it with `POST /api/tasks/2/approve`. Within 5s, pod `grain-sandbox-2-1` appeared in
   namespace `grain`:
   - image `ghcr.io/bwsalmon/grain/k8s-sandbox:main`;
   - not privileged;
   - requests and limits `cpu 2, memory 8Gi, ephemeral-storage 30Gi`.
   It stayed `Pending` with `FailedScheduling: 0/1 nodes are available: 1 Insufficient memory`,
   because the node has 8129388Ki allocatable. The task stayed `running` (**F8**).
4. Ran `PUT /api/settings {"sandboxMemoryMb":2048,"sandboxDiskGb":10}`. That applied live but did
   not move the pending pod. After `kubectl delete pod grain-sandbox-2-1`:
   - the daemon logged `sandbox pod "grain-sandbox-2-1" will never be ready: it is Missing`;
   - run 2-2's pod was **Scheduled, Pulled, Created, Started**;
   - the daemon prepared it and cloned `grain-walkthrough/demo` through its git proxy;
   - the clone failed (`exit=128`) because the token is a dummy and the repo does not exist, which
     is the stubbed GitHub boundary;
   - the pod was then stopped (`Killing`).
   The sandbox setup steps also logged `sudo: not found` four times (**F9**). The fake agent was
   never reached, because the clone comes first. This is the same boundary the linux-container
   walkthrough hit.

## Comparison with the docs

- `docs/grain-init-platforms.md`:
  - **Correct:** `orchestrator.K8sSandboxes`, no privileged pods (the sandbox pod had no
    `privileged`), the namespace at `enforce=baseline`, and the `NET_BIND_SERVICE` fix (the pod
    starts).
  - **Missing, now added:** that a node must fit one default sandbox, 2 CPU / 8 GiB (F8).
  - The plan calls the backend `orchestrator.KubernetesSandboxes`. No such type exists; the doc's
    name is right.
- `docs/k8s-init-design.md`:
  - **Matches:** preflight then apply once, StepSkipped on re-run, `kubectl rollout status`, the
    self-managed port-forward (the documented divergence), the admin password under the
    operator's `-data-dir`, and scratch manifests under `<data-dir>/k8s-preflight/`.
  - **Added:** a "Found by the 10-04 walkthrough" list covering F2 to F5.
- `docs/grain-init.md`:
  - Its flags section still said `linux-kvm` is "the only one implemented". Fixed.
  - It said `-admin-account` defaults to "your OS username". Fixed (F3).
  - It did not mention `-github-credential-name` at all. Fixed (F1).

## Findings

Each has a deterministic reproduction. F1 to F5 and F7 are fixed on this branch with regression
tests.

- **F1: a token without `-github-credential-name` is refused in Go field names.** Repro: command
  5. **Fixed:** `cmd/grain/init.go` now refuses it naming `-github-credential-name`, and
  `docs/grain-init.md` documents the flag. Test: `TestCmdInitNamesTheCredentialNameFlag`.
- **F2: an unpullable sandbox image is also reported as a broken storage class.** Repro: command 7.
  The storage check's consuming pod runs `-k8s-sandbox-image`, so when the image does not pull, the
  pod never becomes Ready, even though the PVC bound. **Fixed:** on a Ready timeout, the check reads
  the PVC phase. When it is `Bound`, the step says the class is fine and names the pod's waiting
  reason. Test: `TestCheckK8sStorageClassRWOBlamesThePodNotTheClassWhenThePVCBound`.
- **F3: the provisioned admin is not an admin.** Repro: run 8 with no `-admin-account`, then sign
  in as `debian`: `isAdmin: false`, and `/api/secrets` returns 403. `/api/settings` shows
  `adminAccounts: ["grain"]`.
  - `-admin-account` defaulted to the user running init.
  - The daemon seeds *its own* OS account as admin. That is the image's uid 1000 `grain` on both
    manifests (`runAsUser: 1000`). Since `151cdebd7` (`main` only), it is also `-daemon-user`
    (`grain`) on the host platforms, so the same default was wrong there too.
  - **Fixed:** an unset `-admin-account` now defaults to:
    - a `-daemon-arg -actor` value, when given;
    - otherwise `grain` on k8s-container and k8s-granule;
    - otherwise `-daemon-user`, or the invoking user when that is `root`.

  Test: `TestDefaultAdminAccountIsTheDaemonsActor`. The live fix is run 10, and the Playwright
  sign-in confirms `isAdmin: true`.
- **F4: `-github-token-file` is silently dropped on the Kubernetes platforms.** Repro: run 8 shows
  no credential step and exits 0, and `POST /api/tasks` into `-github-repo` is refused with
  "team "default" has no GitHub credential".
  - `ensureCredentialLadder` writes under `-data-dir`. On these platforms that directory is on the
    operator's machine, so `initFor` never runs the step.
  - **Fixed:** after `ProvisionOverAPI`, with the port-forward still up,
    `ensureK8sTeamGitHubCredential` PUTs the token to `/api/teams/default/github-credential`. It
    skips a team that already has one.
  - This is compatible with candidate's `c8463cdfc` (`ensureTeamGitCredential`, being ported to
    `main`), which also skips a team that has a credential.

  Tests: `TestInitK8sContainerStoresTheGitHubTokenOnTheTeam` and
  `TestEnsureK8sTeamGitHubCredentialLeavesAnExistingOneAlone`. Live: runs 12 and 13, then task 2
  was filed.
- **F5: a no-op re-run takes over a minute.** Repro: run 9 took 1m12s. Each scratch pod runs
  `sleep`, which as PID 1 ignores SIGTERM, so every blocking delete sat out the 30s default grace
  period. **Fixed:** scratch deletes pass `--grace-period=1`, and runs 10 to 13 took 14 to 16s. The
  exact-argv tests in `k8s_test.go` and `k8s_granule_test.go` are updated.
- **F6: a re-run deletes `admin-password`.** Repro: run 11. This is the same bug as F4 in
  `2026-10-04-grain-init-platforms.md`. It is fixed by `54b4e8882`, which is on `candidate` only
  and reaches `main` through the automatic port. Not fixed twice.
- **F7: blank lines under "Run these".** Repro: run 14. Manual steps with no `Command` printed an
  empty line. **Fixed:** they print `(<step>: no command -- see its line above)`. Test:
  `TestManualFollowUpsNeverPrintsABlankLine`.
- **F8: a sandbox no node can fit stays Pending forever, with the task `running`.** Repro: the
  trivial task on an 8 GB kind node. Init's budget step says "nothing bounds a run". **Filed**
  (follow-up task): an init check against node allocatable, and/or failing an unschedulable
  acquire. The platforms doc now names the requirement.
- **F9: k8s sandboxes log `sudo: not found` for the CA, DNS and package-cache setup.** Repro: the
  daemon log for run 2-2. **Filed.**
- **F10: preflight follow-up commands are wrong in small ways.** **Filed** as one task:
  - the printed `kubectl describe pod grain-init-preflight-image` targets a pod the check already
    deleted;
  - printed commands drop `-k8s-context` / `-k8s-kubeconfig`;
  - a local build's `-k8s-sandbox-image` default (`k8s-sandbox:latest`) is not the tag
    `grain.yaml` runs (`:main`);
  - the no-kubeconfig refusal starts with five repeated kubectl lines.
- **Not reproduced on the tested commit:** on the published `:main` image (`18c7c6d2b`), a remote
  (`X-Forwarded-For`) caller could self-register (`POST /api/auth/register` returned 200 for
  `mallory` and `mallory2`) even though `adminAccounts: ["grain"]` and `openRegistration: false`.
  - The same request against this commit's daemon returned **403** "registration is closed" (and
    401 for anonymous `/api/teams`), both in the cluster and on a local `grain daemon`.
  - The published `:main` tag lags `main`, so a fresh k8s deployment runs whatever that image
    holds, not the commit `grain init` was built from. Noted, not filed: the tag is CI's to move.
  - The two accounts were left in the throwaway cluster's store.

## Stubs

- **GitHub:** a dummy token (`ghp_dummytoken_walkthrough_0000`) in `/tmp/k8s-init-token` and the
  non-existent repo `grain-walkthrough/demo`. `grain init` takes no GitHub API base, so githubsim
  could not be plugged in. The daemon's clone reached real github.com and failed.
- **Live model sessions:** a custom-agent connection (`framework: custom`) running
  `/data/fake-agent` in the daemon pod. It logs the request and answers
  `{"protocol_version":2,"result":{"final_text":"…"}}`. It was never reached (the clone comes
  first).
- **Reverse proxy:** Playwright's `X-Forwarded-For` header, so the browser signs in as a person
  rather than as the trusted loopback caller. This only adds a header; grain's API is not mocked.
- **Cluster:** `kind` stands in for a real cluster. The daemon image for the sign-in and dispatch
  checks is a local overlay of the tested commit's binary on the published image (see
  Environment).
- Settings changed over the real API for dispatch only: the fake agent backend, and
  `sandboxMemoryMb=2048` / `sandboxDiskGb=10` (F8).

## Fixes on this branch

| Commit | Findings |
|---|---|
| `fix(hostinit): stop blaming the storage class for a sandbox image that will not pull` | F2, F1 |
| `fix(init): provision the password for the account the daemon makes admin` | F3, F5, doc fixes |
| `fix(hostinit): give the default team the GitHub token on Kubernetes platforms` | F4 |
| `fix(init): name a manual step with no command instead of printing a blank line` | F7, doc fixes |

`gofmt -l cmd pkg` is clean. `make security` exited 0 (`go vet ./...`, then
`go test -race ./tests/security/...`: ok in 157s).

## CI verdict

Grain CI run `ed765377b9de16e6` on `f42bcc932` (every fix above, before this report was added):
**success**. Jobs: frontend, go (15m45s), installer, lint, package cache, postgres, terraform,
ui e2e (10m27s). None of the known flakes recurred. The commit that adds this report changes only
this file, and its full-suite verdict is on the pull request.
