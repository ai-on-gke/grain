# 2026-10-05: `grain init` adopt mode, the non-root daemon, the `kind` re-check and `k8s-granule` preflight

This run executes §7 of `docs/test-plans/2026-10-05.md` (task 1998, base `candidate`).

## Environment

- Sandbox: Debian 12 (bookworm), kernel 6.1.0-53-amd64, 4 vCPU, 7.8 GB RAM, systemd running as
  PID 1, passwordless `sudo`, `/dev/kvm` present (`crw-rw---- root kvm`).
- Docker 29.8.2 (server). Go 1.26.6. `kind` v0.32.0. `kubectl` client v1.36.4.
- `kind` cluster `grain`: one control-plane node, Kubernetes v1.36.1, `standard` local-path class.
  The node was made to trust the sandbox package cache's CA, as on 10-04 (copy
  `/usr/local/share/ca-certificates/grain-ca.crt` in, `update-ca-certificates`, restart containerd).
- The invoking user is `debian` (uid 1000, in `docker` and `kvm`). "As root" below means `sudo`.

**Tested commit:** `candidate` at `795d33a1a`. `git branch -r --contains` lists `origin/candidate`
for every §7 commit: `16c69935e`, `d829d45c8`, `151cdebd7`, `b10dd96dd`, `f3d991495`, `54b4e8882`,
`d4523af73`, `c8463cdfc`, `2f46ef118`, `50d015248`, `2607ef117`, `f42bcc932`, `c0256f10e`,
`04e57a48d`, `9f1c00b13` and `c77a083ae`. It does **not** list it for `5bd2827bb` (main only). It
**does** list it for `457da8456`, the port of `5bd2827bb` (see the `k8s-granule` section).

`grain init` was `/tmp/grain`, built with `go build -o /tmp/grain ./cmd/grain` from the tested
commit, and rebuilt after each fix on this branch. The daemon binary was the `bin/grain` that init's
own `make build` step produced from the checkout.

## 1. Host platform, non-root daemon (`-platform=linux-container`)

The common flags `C` are
`-platform=linux-container -repo-root=$PWD -install-claude-cli=false`. The token flags `T` are
`-github-token-file=/tmp/tok -github-credential-name=walkthrough -github-repo=grain-walkthrough/demo`.

| # | Command | Exit | Result |
|---|---|---|---|
| H-0 | `sudo grain init C T -data-dir=/var/lib/grain -dry-run` | 0 | `[would] create the daemon account`; every later account step `[skipped]` "does not exist yet". |
| H-1 | `sudo grain init C T -data-dir=/var/lib/grain` | **10** | 41s. `[done]` for: create the daemon account (`created grain:grain`), add it to docker, sandbox network, build grain, setcap, credential ladder, `chown -R grain:grain /var/lib/grain`, start the daemon, admin password for `grain`, target repos, gitCredential, maxWorkers. The one `[manual]` was the budget (c0256f10e): "3969 MiB of sandbox memory … cannot hold one 8192 MiB sandbox", with the PUT to fix it. |
| H-2 | the printed `curl -X PUT …/api/settings` (`sandboxMemoryMb=6144`, reserve 1792), then H-1 again | **0** | 0.37s. Everything `[skipped]`, `"grain" already has an account; … admin-password … is left in place`. `sha256sum` of `admin-password` identical before and after. Daemon pid and start time unchanged. Budget `[done]`: "one 6144 MiB sandbox fits in 6146 MiB". |
| H-3 | `grain init C -data-dir=/tmp/nr-data -daemon-user=grain2`, as `debian`, no sudo, daemon stopped | **10** | `[manual] create the daemon account`; the follow-up list printed `sudo useradd --system --no-create-home --shell /usr/sbin/nologin --user-group grain2`. It also printed `[manual] grant the grain binary CAP_NET_BIND_SERVICE` for a binary root had already granted it (**H1**). |
| H-4 | `grain init C T -data-dir=/var/lib/grain`, as `debian`, deployment up | **10** | `[manual]` setcap (**H1**). `[manual] write the GitHub credential ladder: … credentials.json: permission denied`. `[manual] provision the admin account's password: could not read /var/lib/grain/admin-password: permission denied`. The last two are expected: the data dir is `grain`'s, mode 0700. |
| H-5 | a root-owned data dir: `sudo timeout 15 grain daemon -data-dir /var/lib/grain-old -ui-addr 127.0.0.1:8430 …` (pre-feature root daemon, exit 124), no unit, then `sudo grain init C -data-dir=/var/lib/grain-old -ui-addr=127.0.0.1:8430 -daemon-arg=-ui-addr=127.0.0.1:8430` | **10** | `[done] chown -R grain:grain /var/lib/grain-old` (afterwards `find ! -user grain` is empty). The unit has `User=grain`, and the daemon runs as `grain`. `[done] created "grain"'s password`, but then `[manual] set the default team's worker slots: 403 user "grain" does not have permission` and `[skipped] … no sandbox budget` (**H2**). |
| H-6 | H-5 with this branch's H2 fix | **10** | `[manual] provision the admin account's password: "grain" has an account but is not an admin of this deployment, whose stored admins are root …`. |
| H-7 | H-6's remedy: stop, remove the unit, re-run H-5 plus `-daemon-arg=-as=root` | **10** | Daemon runs as OS user `grain`, `/api/config` says `actor root, isAdmin true`. `[done] created "root"'s password`, `[done] maxWorkers=1`. The budget step reads the budget again and is `[manual]` (8 GiB default shape on this host). |

Assertions after H-1:

- `id grain`: `uid=997(grain) gid=996(grain) groups=996(grain),997(docker)`. The shell is
  `/usr/sbin/nologin`.
- `/var/lib/grain`, and everything in it, is `grain:grain`. The data dir is mode 0700, and
  `admin-password` is mode 0600 and owned by `grain`.
- `/etc/systemd/system/grain-daemon.service` has `User=grain`, `Group=grain` and
  `ExecStart=/home/debian/work/bin/grain daemon -data-dir /var/lib/grain -container-sandboxes -container-sandbox-network grain-sandboxes -container-git-proxy-host 10.241.0.1`.
- `ps -o user -C grain`: `grain`. `sudo -u grain docker ps` works. The daemon log says
  "serving the UI/API on http://127.0.0.1:8420 as grain".
- **Fake-agent task.** `PUT /api/settings` set
  `agentBackends: [{id: walkthrough-fake, framework: custom, command: /usr/local/bin/grain-fake-agent, models: [fake-model], team: default}]`.
  Then `POST /api/tasks` filed task 2 (repo `grain-walkthrough/demo`), and
  `POST /api/tasks/2/approve` approved it. The task went `queued → running`.
  - The daemon, as `grain`, created the sandbox container over docker, and the package cache was
    configured.
  - The clone through the git proxy then failed with
    `502 … remote: upstream request failed`, because the GitHub token is a stub (see Stubs).
  - So the run reached docker and its store as `grain`. The agent itself was not reached, the
    same limit as 10-04.

## 2. Adopt mode

| # | Command | Exit | Result |
|---|---|---|---|
| A-1 | unit stopped, disabled and removed; `sudo install -d -o grain -g grain -m 700 /var/lib/grain-adopt`; `sudo -u grain sh -c 'nohup bin/grain daemon -data-dir /var/lib/grain-adopt -ui-addr 127.0.0.1:8450 -poll-interval 7s -max-agent-turns 42 -environment-name adopt-walk -container-sandboxes -container-sandbox-network grain-sandboxes -container-git-proxy-host 10.241.0.1 &'` | — | `/api/settings`: `pollInterval 7s`, `maxAgentTurns 42`, `sandboxBackend container`. |
| A-2 | `sudo grain init C -data-dir=/var/lib/grain-adopt -ui-addr=127.0.0.1:8450` | **10** | `[done] start the daemon: adopted the already-running daemon (… pid 13022): wrote … grain-daemon.service matching its actual command line (read from /proc/13022/cmdline) and enabled it`. The account, docker and chown steps are `[skipped]` "adopts it as-is". Only the budget is `[manual]`. |
| A-3 | `sudo kill 13022; sudo systemctl start grain-daemon` | — | `active`, as `grain`. The cmdline is byte-identical to the unit's `ExecStart`, which is byte-identical to A-1's flags. `/api/settings` (`pollInterval`, `maxAgentTurns`, `sandboxBackend`, `adminAccounts: [grain]`) and `/api/config` `actor` are identical before and after (`diff` empty). |
| A-4 | the plan's own form: `setsid nohup sudo -u grain bin/grain daemon …` (no unit), then A-2 | **10** | `adopted … (pid 13336)`. The unit says `User=debian`, `Group=grain`, `ExecStart=sudo -u grain /home/debian/work/bin/grain daemon …` (**H4**). |
| A-5 | A-4 with this branch's fix | **10** | `adopted … (pid 13338)`. `User=grain`, `Group=grain`, and `ExecStart=/home/debian/work/bin/grain daemon -data-dir /var/lib/grain-adopt …`. |
| A-6 | `-daemon-user` upgrade, the documented path: a root-run data dir, plus a hand-written pre-feature unit (no `User=`) running; `sudo grain init C -data-dir=/var/lib/grain-up …` | **10** | Account, docker and chown all `[skipped]` "a daemon is already running … adopts it as-is". The unit is left alone and the data dir stays `root`. This contradicts `docs/grain-init.md` (fixed, see Docs). |
| A-7 | `systemctl stop grain-daemon`, then A-6 again, with the kind cluster up | **10** | `[skipped] … a process matching "grain daemon" is already running (pid 70164) … already managed by … grain-daemon.service -- nothing to adopt`. Nothing was chowned or started, and the API was unreachable. pid 70164 is the kind node's grain pod, `/usr/local/bin/grain daemon -data-dir=/data …`, in another PID namespace (**H6**). |
| A-8 | A-7 with this branch's H6 fix | **10** | `[done] chown -R grain:grain /var/lib/grain-up`, `[done] start the daemon`. The unit was rewritten with `User=grain`, and the daemon runs as `grain`. The admin step is `[manual]` for H2, as intended. |

## 3. `kind`: yesterday's commands 6-14 against `candidate`

The token flags `T` are as above. Each run uses `-platform=k8s-container -repo-root=$PWD`.

| 10-04 # | Command | Exit | Result |
|---|---|---|---|
| 7 (F2) | `T -data-dir=/tmp/k8s-f2 -k8s-sandbox-image=registry.invalid/nope:1` | **10** | 3m03s. `[manual] sandbox image pulls (ImagePullBackOff)`. `[manual] storage class offers ReadWriteOnce: a scratch PersistentVolumeClaim … bound, but the pod mounting it … did not become Ready … (ImagePullBackOff) -- the class looks fine; fix whatever stops that pod starting`. **F2 fixed.** The printed `kubectl describe pod` lines still name deleted pods (F10, filed 10-04). |
| 6/8 | `T -data-dir=/tmp/k8s-init`, first full run (CA already trusted, so 6 and 8 are one run) | **0** | 45s. Preflight all `[skipped]` (passed). `[done]` for: apply, rollout, port-forward, API reachable, `created "grain"'s password` (**F3 fixed**), target repos, maxWorkers. But it also printed `[done] point the default team at the GitHub credential: gitCredential=walkthrough` and then `[skipped] give the default team the GitHub token: the default team already has one (walkthrough)` (**K1**). |
| — | `kubectl -n grain exec deploy/grain -- ls /data/secrets/github/` | 0 | Only `.team-git-credentials-migrated`: no ladder, and no token. `GET /api/teams/default` returned `gitCredential: walkthrough` (K1). |
| 12 | team credential cleared (`PUT /api/teams/default {"gitCredential":""}`), then with the K1 fix | **0** | 14.3s. `[skipped] point the default team at the GitHub credential: skipped: -platform=k8s-container has no credential ladder in the cluster …`. `[done] give the default team the GitHub token: stored as the default team's own GitHub credential`. **F4 fixed** (with K1). |
| 13 | as 12 | **0** | 14.0s (**F5 holds**). `[skipped] … already has one (grain-team-ZGVmYXVsdA)`. `admin-password` "is left in place" (**F6 holds**). |
| 14 (F7) | view-only SA (`default:limited`, ClusterRole `view`), `-k8s-kubeconfig=/tmp/limited.kubeconfig -k8s-context=limited` | **10** | `[manual]` permissions, `[refused]` image pulls, `[manual]` storage. "Run these" printed `(permission to create what grain.yaml creates: no command -- see its line above)` and `(storage class offers ReadWriteOnce: no command -- see its line above)`. No blank lines: **F7 fixed.** |

**F3 sign-in.** `POST /api/auth/login` as `grain` with the provisioned password, over
`kubectl port-forward` with `X-Forwarded-For: 203.0.113.5`, returned 200. `/api/config` then said
`actor grain, authenticated true, isAdmin true`, and `/api/secrets` returned 200. This was run on
the overlay image described below.

**The published `:main` image trusts a forwarded caller as the daemon (K2).** `grain.yaml` names
`ghcr.io/bwsalmon/grain/grain:main`. Its `org.opencontainers.image.revision` is still `18c7c6d2b`
(created 2026-10-04 06:42 UTC), the same image 10-04 ran.
- On that image, an **anonymous** request through the port-forward with
  `X-Forwarded-For: 203.0.113.5` got `actor grain, isAdmin true`. It got 200 from `/api/teams`,
  `/api/secrets` and `/api/tasks`.
- The same requests against `candidate`'s daemon on the host returned `authenticated false` and 401
  (also with `Forwarded:` and `X-Real-IP:`).
- So did an overlay image: the published image with `/usr/local/bin/grain` replaced by a
  `CGO_ENABLED=0` build of this branch, tagged `ghcr.io/bwsalmon/grain/grain:walk1998`, loaded with
  `kind load` and `kubectl set image`.

The code on `candidate` is right. What a fresh `k8s-container` or `k8s-granule` deployment runs is
the stale tag. Filed as a follow-up.

**F4 dispatch.** On the overlay image, the fake agent was copied to `/data/fake-agent`, and
`sandboxMemoryMb=2048` / `sandboxDiskGb=10` were set for F8. Task 2 was then filed and approved. Its
clone got past the credential check, reached the git proxy, and failed with `502 upstream request
failed`. That is the GitHub stub, not "team has no GitHub credential" as on 10-04.

**F8 (kind) is not fixed, and is not claimed fixed.** `c0256f10e` is the *host* budget check from
`2026-10-04-grain-init-platforms.md` (H-1 above shows it working). On `k8s-container` and
`k8s-granule`, the step says `[skipped] the daemon reports no sandbox budget on this host -- nothing
bounds a run`. An 8 GiB default sandbox still cannot fit this 7.8 GB node. 10-04 filed F8 as its own
follow-up, so it is not filed again. `docs/grain-init-platforms.md` now says init does not check it.

**F9 still happens.** Every sandbox pod setup logs:
`writing CA cert … sh: 7: sudo: not found`, `updating CA certificates … sudo: not found`,
`configuring DNS /etc/resolv.conf … sudo: not found`, and the same for the package cache's apt
config (daemon log for runs 2-1 and 2-2).
- `pkg/orchestrator/packagecache.go` goes through `runAsRoot`, which only reaches root where the
  transport has `SandboxRootCommander` (`docker exec -u 0`).
- `kubectl exec` has no user switch, and the k8s sandbox image runs unprivileged with no `sudo`.
- A fix needs a pod-spec change in `pkg/orchestrator`, a trust-boundary path: a root init
  container, or `dnsConfig` plus a mounted CA. That is not scoped. 10-04 filed it, so it is not
  filed again.

## 4. `k8s-granule` on `kind`

`/dev/kvm` exists in this sandbox and in the kind node. The no-KVM case was made by
`docker exec grain-control-plane mv /dev/kvm /dev/kvm.off`. That changes the node container's own
`/dev` tmpfs, not the host. The common flags are
`-platform=k8s-granule -repo-root=$PWD -data-dir=/tmp/k8s-gran-nokvm T`, after
`kubectl delete ns grain`.

| # | Command | Exit | Result |
|---|---|---|---|
| G-1 | no namespace | **10** | `[manual] namespace pod-security level: namespace "grain" does not exist yet -- grain init never creates or labels …`, with the `kubectl create namespace … && kubectl label … enforce=privileged` command. |
| G-2 | namespace created with `enforce=privileged`; no `/dev/kvm` on the node | **10** | 31s. `[manual] nested virtualization (scheduled boot): … the container already exited … a likely sign that "ghcr.io/bwsalmon/grain/guest:latest" is not a packed guest …`. The log showed `cloud-hypervisor … CreateHypervisor(VmCreate(Is a directory (os error 21)))`. Afterwards `ls -ld /dev/kvm` on the node showed **`drwxr-xr-x` (a directory)**: the kubelet had created it for the untyped hostPath (**G1**). |
| G-3 | stray directory removed, kvm device back on the node (the node's `/dev` was repopulated) | **0** | 49s. The nested-virt check `[skipped]`: "a privileged pod … booted a VM over /dev/kvm and answered ready". Then apply, rollout, port-forward, password for `grain`, repos, maxWorkers, and `[done] give the default team the GitHub token`. The daemon pod is `1/1 Running`, 0 restarts, and logs "upgrading from the UI is off -- neither -upgrade-image nor -upgrade-src-dir is set". **No CrashLoop.** |
| G-4 | G1 fix, `/dev/kvm` hidden again | **10** | 32s. `[refused] nested virtualization (scheduled boot): the node the pod was scheduled on has no /dev/kvm character device, so no sandbox pod can boot a VM there: enable nested virtualization …`. Events: `FailedMount … hostPath type check failed: /dev/kvm is not a character device`. No directory was left on the node. |

**The plan's expected CrashLoop did not happen.** It expected `candidate`'s `grain-granule.yaml` to
still pass `-upgrade-image`. It no longer does: `457da8456` ("fix(k8s): stop grain-granule.yaml
passing a flag its own daemon refuses", the port of `5bd2827bb`) is on `origin/candidate`. G-3 is
the full run that confirms it. Nothing was cherry-picked. §10's "neither fix has a port on
`candidate`" is out of date for `5bd2827bb`.

## Findings

Each has a deterministic reproduction. H1, H2, H4, H5, H6, K1 and G1 are fixed on this branch with
regression tests. K2 is filed. F8 and F9 were already filed on 10-04.

- **H1: a non-root re-run reports an already-granted `CAP_NET_BIND_SERVICE` as manual.**
  - Repro: H-1 as root, then H-4 as `debian`. That exits 10 with `[manual] grant the grain binary
    CAP_NET_BIND_SERVICE`, although `/usr/sbin/getcap bin/grain` prints `cap_net_bind_service=ep`.
  - Cause: Debian keeps `/usr/sbin` off a non-root user's `PATH`, so `getcap` was "not found".
  - **Fixed:** `RealSystem.Run` looks in `/usr/local/sbin`, `/usr/sbin` and `/sbin` for a command
    on no `PATH`. Test: `TestRealSystemRunFindsSbinToolsOffPath`. Live: the same non-root run now
    says `[skipped] … already granted`.
- **H2: upgrading a root-seeded data dir leaves nobody admin who can sign in.**
  - Repro: H-5.
  - `adminAccounts` stays `["root"]`, because the daemon seeds its actor once, on first start.
    After the chown, the daemon acts as `grain`.
  - Init gave `grain` a password and called it `[done]`. The worker-slot PUT then got 403, and the
    budget step skipped. A non-admin's `GET /api/settings` carries no `sandboxCommitment`; that was
    seen on H-5, but not on A-8, whose settings did carry a budget, and the cause is not pinned.
  - **Fixed (reported, not migrated):** after registering, `ensureAdminAccount` reads
    `/api/config`'s `adminAccounts`. If `-admin-account` is not among them, it is `[manual]`, naming
    them and the `-daemon-arg=-as=<admin>` re-run. H-7 shows that re-run works.
  - Test: `TestEnsureAdminAccountReportsAnAccountThatIsNotAnAdmin`. An automatic migration is a
    follow-up.
- **H4: adopt mode adopts `sudo`, not the daemon.**
  - Repro: A-4. The plan's own `sudo -u grain grain daemon …` is two processes, and `pgrep -f`
    lists sudo first. The unit was written with `ExecStart=sudo -u grain …` and `User=debian`.
  - **Fixed:** `pickDaemonPID` takes the match whose argv is `<…grain…> daemon`, and drops a
    readable non-daemon match. An unreadable match still reaches the refusal.
  - Tests: `TestEnsureDaemonAdoptsTheDaemonNotItsSudoWrapper` and
    `TestDaemonAlreadyRunningIgnoresAMatchThatIsNotADaemon`. Live: A-5.
- **H5: `-daemon-arg -actor` is not a daemon flag.**
  - Repro: `grain daemon -actor root -data-dir /tmp/x` logs `WARNING: starting anyway, ignoring 1
    flag(s) this build does not define: -actor`.
  - `defaultAdminAccount` (F3's fix) and `-admin-account`'s help both named `-actor`, so a run with
    it provisioned a password for an account the daemon never made admin. The daemon's flag is
    `-as`.
  - **Fixed:** `daemonArgActor` reads `-as`, and the help text, the package comment and the doc now
    say `-as`. Test: `TestDefaultAdminAccountIsTheDaemonsActor` (the `-as` cases, plus `-actor`
    falling through to the default).
- **H6: a containerized grain daemon is taken for this host's.**
  - Repro: A-7, with a kind cluster running a grain pod.
  - **Fixed:** `pickDaemonPID` drops a match in another PID namespace (`/proc/<pid>/ns/pid`). One
    it cannot read is not dropped. Tests: `TestDaemonAlreadyRunningIgnoresADaemonInAnotherPIDNamespace`;
    the `/proc` fakes now pin the namespace, so no test depends on a real pid. Live: A-8.
- **K1: on Kubernetes the default team is pointed at a credential the cluster never had, so F4's
  token step skips.**
  - Repro: run 6/8 and the `exec ls` after it.
  - `c8463cdfc`'s host step PUT `gitCredential=walkthrough` before `2607ef117`'s step, which then
    saw "already has one". The 10-04 F4 test missed it because its fake `GET /api/teams/default`
    never reflected the earlier PUT.
  - **Fixed:** the host step is `[skipped]` on `k8s-container` and `k8s-granule`.
    `TestInitK8sContainerStoresTheGitHubTokenOnTheTeam` now fails on any such PUT. Live: runs 12
    and 13.
- **G1: `/dev/kvm` as an untyped hostPath.**
  - Repro: G-2. A node without the device got an empty directory created at `/dev/kvm`, and the
    preflight blamed the image.
  - The real sandbox pods (`pkg/k8s` `PodOptions.HostDevices`) used the same untyped mount.
  - **Fixed:** both are `type: CharDevice`. The preflight reads the `hostPath type check failed`
    event and is `[refused]`, naming `/dev/kvm`.
  - Tests: `TestCheckK8sGranuleNestedVirtRefusesANodeWithNoKVMDevice` (`pkg/hostinit`), and the
    hostPath type assertion in `pkg/k8s` `k8s_test.go`. Live: G-4.
- **K2: the published `grain:main` image is about 30 hours stale and trusts a forwarded anonymous
  caller as admin.** Repro: see the `kind` section. **Filed** as a follow-up. 10-04 noted the stale
  tag but did not file it.
- **Minor, not fixed:** in H-0's dry run, `start the daemon` says "skipped: the account … is not
  ready yet", where it would in fact start. The dry-run plan reads as if the daemon never starts.

## Docs compared (step 5)

- `docs/grain-init.md`:
  - **Fixed:** "Upgrading a deployment … as root" said a re-run "chowns `-data-dir` … regardless of
    whether a daemon is currently running". A-6 shows the account and chown steps are `[skipped]`
    while it runs. The section now describes A-6 to A-8, the stored `root` admin (H2), and the
    `-daemon-arg=-as=root` remedy.
  - **Added:** which process adopt mode adopts (H4, H6).
  - **Fixed:** the team-credential bullet in "What it does, in order" is host-only on Kubernetes
    (K1).
  - **Fixed:** `-daemon-arg -actor` changed to `-as` (H5).
  - **Matches:** the account, docker group, chown, `User=`, exit codes 0/10, the password kept on
    re-run, the budget step's text and PUT, and the dry-run plan.
- `docs/grain-init-platforms.md`:
  - **Added:** the k8s-container row says init does not check that a node fits a sandbox (F8 open).
  - **Added:** the k8s-granule row records G-3's end-to-end `kind` run and G1's refusal.
  - **Matches:** the `linux-container` row (no `/dev/kvm`, docker only), and k8s-granule's
    privileged namespace requirement (G-1).

## Stubs

- **GitHub:** a dummy token (`ghp_dummytoken_walkthrough_0000`) in `/tmp/tok`, and the non-existent
  repo `grain-walkthrough/demo`. `grain init` takes no GitHub API base, so githubsim could not be
  plugged in. Clones reached the git proxy and failed upstream with 502.
- **Live model sessions:** a custom-agent connection (`framework: custom`), running
  `/usr/local/bin/grain-fake-agent` on the host and `/data/fake-agent` in the pod. It answers
  `{"protocol_version":2,"result":{"final_text":"fake agent done"}}`. It was never reached, because
  the clone comes first.
- **Remote caller:** an `X-Forwarded-For: 203.0.113.5` header on curl requests. It only adds a
  header; grain's API is not mocked.
- **Cluster:** `kind` stands in for a real cluster. "No `/dev/kvm`" is the device moved aside in the
  kind node's own `/dev`. The overlay image `grain:walk1998` is this branch's binary on the
  published image.
- Settings changed over the real API for dispatch only: the fake agent backend, the host budget fix
  (`sandboxMemoryMb=6144`, `sandboxMemoryReserveMb=1792`), and in kind `sandboxMemoryMb=2048` /
  `sandboxDiskGb=10`.

## Fixes on this branch

| Commit | Findings |
|---|---|
| `fix(hostinit): adopt the daemon itself, not a sudo wrapper, and find sbin tools without root` | H4, H1 |
| `fix(init): read the daemon's -as, and report an admin account the daemon does not make admin` | H5, H2 |
| `fix(hostinit): stop pointing a k8s default team at a credential the cluster never had` | K1 |
| `fix(k8s-granule): mount /dev/kvm as a CharDevice and refuse a node without one` | G1 |
| `fix(hostinit): ignore a containerized grain daemon when looking for this host's own` | H6, docs |

Run locally:
- `gofmt -l pkg cmd` is clean.
- `go test ./pkg/hostinit ./pkg/k8s ./tests/deploy`, and `./pkg/orchestrator -run 'Granule|K8s'`,
  pass.
- Each new regression test was run against the pre-fix code and failed there.
- Locally, `go test ./cmd/grain` fails `TestParseDaemonArgsDatabaseFlags` and
  `TestKonturFlagAliasesDeprecationLogging` with "open /var/lib/grain/db-config.json: permission
  denied". That is this walkthrough's own `/var/lib/grain` (owned by `grain`, mode 0700) under the
  default data dir, not the code. With it moved aside, `go test ./cmd/grain` passes.

## CI verdict

Grain CI run `d5de711133cb2173` on `68e18a2` (every fix above plus this report, merged with
`candidate`): **success**. Jobs: frontend (5m04s), go (15m46s), installer, lint, package cache,
postgres, terraform, and ui e2e (11m52s). None of the known flakes recurred. The commit adding this
verdict changes only this file, and its own full-suite run is the one the pull request reads.
