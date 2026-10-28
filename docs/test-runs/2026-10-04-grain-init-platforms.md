# `grain init` platforms on `candidate`: walkthrough, 2026-10-04

Plan: `docs/test-plans/2026-10-04.md` §6. Task 1832.

## Environment

- Debian GNU/Linux 12 (bookworm), kernel `6.1.0-53-amd64` (`#1 SMP PREEMPT_DYNAMIC Debian
  6.1.187-1`), x86_64, 4 vCPU, 7.8 GiB RAM, systemd running as PID 1.
- `/dev/kvm` **exists** on this host (`crw-rw---- root:kvm`, nested virt `kvm_intel nested=Y`), and
  the `debian` account is in `kvm` and `docker`. The plan assumed no KVM, so "a host without
  `/dev/kvm`" was simulated with `/tmp/t/nokvm.sh`. It runs the command as `debian` in a private
  mount namespace whose `/dev` is a tmpfs holding only `null zero random urandom tty full`, `fd`,
  `std*`, `pts` and `shm`, so `test -e /dev/kvm` fails for it alone.
- docker-ce 29.8.2 (already installed from Docker's apt repository), Go 1.26.6 (matches `go.mod`),
  GNU Make 4.3, node v20.19.0, npm 10.8.2, Playwright 1.62.1 (Chromium from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).

## Tested commit

`candidate` at `9b06116c4` (the merge base of this branch). Every commit §6 covers is on it:
`git branch -r --contains` lists `origin/candidate` for `f9c86575f`, `726d96af3`, `4037f11b6`,
`2461e9abc`, `2bd718a7b`, `d6ee8e6ff`, `7470ef0d3`, `ad1b271fe`, `5d839e431` and `3480ae39a`.
The fixes below were then re-verified on the same host at this branch's own commits.

`/tmp/grain` was built with `go build -o /tmp/grain ./cmd/grain`. The daemon binary is whatever
`grain init` builds into `<repo-root>/bin/grain` (`make build`).

## Commands and exit codes

`A` below is `-platform=linux-container -repo-root /home/debian/work -data-dir /var/lib/grain`, and
`T` is `-github-token-file /tmp/t/dummy-token -github-credential-name walkthrough -github-repo
walkthrough-org/hello`.

| # | Command | Exit | Outcome |
|---|---------|------|---------|
| 1 | `nokvm.sh /tmp/grain init -platform=linux-kvm -repo-root … -data-dir /tmp/t/d-kvm` | **10** | make and npm `[skipped] already installed`; `[manual] /dev/kvm` with `sudo modprobe kvm_intel \|\| sudo modprobe kvm_amd`. Nothing else ran and no data dir was created. No mention of `linux-container` (**F1**). |
| 2 | `/tmp/grain init -platform=k8s-granule …` | **10** | `[refused] platform -platform=k8s-granule is not implemented yet: …` naming nested-virt nodes, `/dev/kvm` in the pod and privileged pods. |
| 3 | `/tmp/grain init -platform=k8s-container …` | **10** | `[refused] platform …` naming the cluster, a pullable image, RWO storage and a NetworkPolicy CNI, pointing at `docs/k8s-init-design.md`. |
| 4 | `/tmp/grain init -platform=bogus …` | **1** | `hostinit: "bogus" is not a platform grain init has a name for (know: [...])`. The typo "know" is fixed. |
| 5 | `sudo /tmp/grain init A` (no token) | **0** | 55 s. Network created (gateway 10.241.0.1), grain built, setcap, claude CLI installed, daemon started, admin password created, maxWorkers=1. `[skipped]` for the credential ladder and target repos. The plan expected "steps remain"; see **F7**. |
| 6 | `sudo /tmp/grain init A -github-token-file … -github-repo …` (no credential name) | **1** | `hostinit: GitHubCredentialName is required with GitHubToken` names a Go field rather than a flag (**F3**). |
| 7 | `sudo /tmp/grain init A T` (2nd run) | **10** | Ladder `[done]`, target repos `[done]`, but `[refused] start the daemon … already running (systemd unit grain-daemon.service is active)` (**F2**). It also silently deleted `admin-password` (**F4**). |
| 8 | `sudo /tmp/grain init A T` (3rd, idempotent run) | **10** | Same refusal; the ladder rewritten and reported `[done]` again (**F2**). |
| 9 | as 8, with fix F2 and an uncommitted tree | 10 | `[done] build grain` (dirty tree), then `[manual] start the daemon … restart it`, `systemctl restart grain-daemon.service`. |
| 10 | as 8, fixes committed, after the rebuild and restart | **0** | Every step `[skipped]` except `[done] confirm the daemon's API is reachable` (a read-only probe). Nothing rebuilt. |
| 11 | same again | **0** | 16 `[skipped]`, the same single probe, nothing rebuilt. |
| 12 | Docs literally: `./bin/grain init -repo-root . -data-dir /var/lib/grain` as `debian` | **10** | Default `linux-kvm` (KVM present): built the granule CLI, `granule:vendored` and `granule-guest-base:vendored`. `[manual] guest image` failed (sandbox TLS interception, see "Could not run"), plus `[manual]` setcap and claude CLI (`mkdir /var/lib/grain: permission denied`). The daemon was skipped. |
| 13 | Docs literally plus root and container: `sudo ./bin/grain init -platform=linux-container -repo-root . -data-dir /var/lib/grain` | **10** | `[manual] start the daemon: Unit grain-daemon.service has a bad unit file setting`, because the unit had `ExecStart=./bin/grain …` (**F5**). |
| 14 | as 13, re-run with T | **10** | `[manual] build grain … could not write ./bin/grain: text file busy` (**F5**). |
| 15 | as 13 with all fixes, clean data dir, no token | **0** | Unit `ExecStart=/home/debian/work/bin/grain …`, daemon up, password created. |
| 16, 17 | as 15, re-run twice with T | **0**, **0** | `admin-password` sha256 unchanged across both runs, and the step says the file "is left in place". |
| 18 | fresh data dir, T on the very first run (before fix F6) | **0** | Default team `targetRepos=[walkthrough-org/hello]`, `gitCredential=null`. `POST /api/tasks` into that repo was refused: `team "default" has no GitHub credential` (**F6**). |
| 19 | as 18 with fix F6, then a re-run | **0**, **0** | `[done] point the default team at the GitHub credential gitCredential=walkthrough`. Filing a task into `walkthrough-org/hello` succeeds. |

## Browser and dispatch (Playwright, real daemon)

The daemon on `127.0.0.1:8420` was started by `grain init` (run 15). It was driven by Playwright
with `extraHTTPHeaders: {"X-Forwarded-For": "203.0.113.5"}`. A loopback request with no
forwarding header is trusted as the daemon itself (`pkg/ui/principal.go` `isTrustedSource`), and
the header makes the browser a person behind a reverse proxy, so the sign-in is real.

- Anonymous `GET /api/teams`: **401**. The landing page shows "sign in to use this server's API".
- Sign in as `root` with a wrong password: "invalid username or password".
- Sign in as `root` with the contents of `/var/lib/grain/admin-password`: the form goes away, and
  `GET /api/teams` returns 200 with the default team (`id: default`) at `maxWorkers: 1` and
  `targetRepos: ["walkthrough-org/hello"]`, plus a personal team `root` at `maxWorkers: 0`.
- Sandbox health (`GET /api/sandboxes`): 200, `enabled: true`, host 4 CPU / 7938 MB, disk floor
  6039 MB, `nestedVirt: enabled`, daemon `flagSource: "this host's grain-daemon.service"`. Its
  `flagFix` text says "Re-running scripts/setup.sh on the host rewrites the unit", which is the
  wrong tool for a `grain init` host (noted, not fixed).
- Trivial task: after `PUT /api/settings {"sandboxMemoryReserveMb":512,"sandboxMemoryMb":2048}`
  (**F8**) and a fake custom-agent connection, task 2 ("walkthrough: trivial task") was approved
  and dispatched. **A docker container sandbox was created**: `grain-sandbox-2-1`, image
  `ghcr.io/bwsalmon/grain/k8s-sandbox:latest`, on `grain-sandboxes`, then `grain-sandbox-2-2` on
  retry. Preparing it, the clone through the git proxy reached real github.com, which answered
  **401** for the dummy token. The task was parked in `awaiting_reply` with the proxy's message
  ("GitHub refused the credential itself (401) … walkthrough"). That is the external boundary. The
  fake agent was never reached, since the clone comes first. The journal also shows **F9** for
  every run.

## `scripts/setup.sh` host mode

`bash -n scripts/setup.sh` and `shellcheck scripts/setup.sh` were both clean. The script ran in a
throwaway `debian:bookworm` container with systemd as PID 1 (`--privileged --cgroupns=host`, the
host docker socket mounted, the checkout read-only at `/src`):

    GRAIN_INSTALL_MODE=host GRAIN_HOST_BINARY=/usr/local/src-grain GRAIN_HOST_SOURCE_DIR=/src bash /src/scripts/setup.sh

It exited **0** twice. `grain-daemon` was active and running as `grain` (uid 997). After each run,
`find … -user root` found **0** entries under `/var/lib/grain` and `/var/lib/grain-sandbox`, both
owned `grain:grain`. Only `/etc/grain` and `/etc/grain/image.env` are root-owned, which is
deliberate: root's own config.

## Findings

Each has a deterministic reproduction. F1–F6 are fixed here with fake-system regression tests in
`pkg/hostinit`.

- **F1: the linux-kvm refusal does not mention linux-container.** Repro: command 1. **Fixed**: the
  `/dev/kvm` detail and command now say to re-run with `-platform=linux-container`.
  `TestPreflightKVMRefusalPointsAtLinuxContainer`.
- **F2: no re-run can ever exit 0.** Repro: commands 5, 7 and 8. `ensureDaemon` refused "a second
  daemon" when the running one was the `grain-daemon.service` the first run had installed. The
  credential ladder was also rewritten and reported `[done]` on every run. **Fixed**: an active
  unit whose file is byte-for-byte what this run would write counts as `[skipped]`. If this run
  rebuilt the binary, the step is `[manual]` with `systemctl restart grain-daemon.service`. Any
  other daemon is still refused, and an unchanged ladder is `[skipped]`. Tests:
  `TestEnsureDaemonSkipsItsOwnRunningUnit`,
  `TestEnsureDaemonRefusesActiveUnitItWouldNotHaveWritten`,
  `TestEnsureDaemonAsksForRestartAfterRebuildingTheBinary` and
  `TestEnsureCredentialLadderSkipsWhenAlreadyWritten`.
- **F3: the missing credential name error names a Go field.** Repro: command 6. **Fixed**: the
  error now names `-github-credential-name`, and the flag is documented.
- **F4: a re-run deletes the only copy of the admin password.** Repro: command 5 creates
  `/var/lib/grain/admin-password`, command 7 removes it, and nobody can sign in. On a re-run,
  `ensureAdminAccount` wrote a new random password over the file, got 409, then `os.Remove`d it.
  **Fixed**: an existing file is reused, never replaced. On 409 it is kept, and on 200 it is the
  password registered. Only a password generated in the same run is removed on 409. Tests:
  `TestEnsureAdminAccountRerunKeepsThePasswordFile` and
  `TestEnsureAdminAccountRetriesWithTheFileItAlreadyWrote`.
- **F5: the documented `-repo-root .` breaks the daemon and every re-run.** Repro: commands 13 and
  14. The relative path reached the unit as `ExecStart=./bin/grain`, which systemd rejects. Then
  `"./bin/grain" != "bin/grain"` made `ensureGrainBinary` copy the binary onto itself ("text file
  busy"). **Fixed**: `Options.withDefaults` makes every path absolute.
  `TestWithDefaultsMakesEveryPathAbsolute`.
- **F6: a deployment init reports up cannot take a task into `-github-repo`.** Repro: command 18,
  then `POST /api/tasks {"repo":"walkthrough-org/hello","team":"default"}` gives `team "default"
  has no GitHub credential`. Since task 1592, teams don't fall back to the deployment ladder, and
  the one-time migration runs before init has set any target repo. **Fixed**: a new provisioning
  step sets the default team's `gitCredential` to `-github-credential-name` over the loopback API
  when the team has none. Tests: `TestEnsureTeamGitCredentialPointsTheDefaultTeamAtTheToken`,
  `…LeavesAnExistingOneAlone` and `…SkipsWithNoToken`.
- **F7: exit 0 with no token and no `-github-repo`.** Repro: command 5. The plan expected "steps
  remain". The code counts both as deliberate skips and points to the UI. **Documented** (the exit
  codes section now says so) rather than changed, because exit 10 would make the configure-in-UI
  path report failure forever. A refused `-platform` also exits 10 although no re-run helps; that
  is documented too.
- **F8: init reports a host up whose sandbox budget can never admit a run.** Repro: run 15 on this
  7.8 GiB host. The daemon logs `0.0 GiB of sandbox memory (7.8 GiB host less 8.0 GiB reserved)`
  against an 8 GiB sandbox shape, so approved tasks stay queued while init said 0. **Filed** as a
  proposal: init should report it `[manual]` or size the settings.
- **F9: container sandboxes never get the package cache.** Repro: dispatch any task on
  `linux-container` and the journal shows `writing /etc/apt/apt.conf.d/01-grain-package-cache in
  2-1: sh: 7: sudo: not found`. `docker run --rm --entrypoint sh
  ghcr.io/bwsalmon/grain/k8s-sandbox:latest -c 'id; command -v sudo'` gives `uid=1000(agent)` and
  no sudo. **Filed** as a proposal (`pkg/orchestrator`, trust boundary).
- **F10: a root run leaves root-owned build output in the checkout.** Repro: command 5 as root in
  `debian`'s checkout leaves `bin/`, `pkg/ui/static/**` and `ui/node_modules` owned by root, and
  `npm ci --omit=dev` strips dev dependencies (Playwright) from `ui/node_modules`. **Documented**:
  use a checkout kept for the deployment. Related: any untracked file in the checkout makes
  `grainBinaryCurrent` false (`git status --porcelain`), so such a checkout rebuilds, and with F2's
  fix asks for a restart, on every run.

### Doc mismatches in `docs/grain-init.md`, fixed directly

- The example did not say to run as root. Without root every install, setcap and daemon step is
  `[manual]`.
- `-platform` said `linux-kvm` was "the only one implemented".
- `-github-credential-name`, which a token needs, and `-github-repo` were missing from Flags.
- Exit 0 without a token or repos, and exit 10 for a refused platform, were not described.
- "What it does, in order" listed `/dev/kvm` as checked on every platform, and lacked the new
  team-credential step.
- The admin password section said a re-run "never overwrites it", which was false before F4.
- `grain init -help` and the `pkg/hostinit` package doc were updated the same way.

## Stubs

- **GitHub**: a dummy `ghp_…` token (random hex) in `/tmp/t/dummy-token`, and a non-existent repo
  `walkthrough-org/hello`. `grain init` takes no GitHub API base, so githubsim could not be
  plugged in. Init never calls GitHub itself; the daemon's git proxy reached real github.com and
  got 401.
- **Live model sessions**: a custom-agent connection (`framework: custom`, protocol v2 per
  `docs/custom-agent-backends.md`) pointing at `/usr/local/bin/grain-fake-agent`, which logs the
  request and answers `{"protocol_version":2,"result":{"final_text":"…"}}`. It was never reached
  (clone first).
- **Reverse proxy**: Playwright's `X-Forwarded-For` header, to sign in as a person rather than as
  the trusted loopback caller. This adds a header to a real request; grain's API is not mocked.
- **No KVM**: simulated with the mount namespace above.
- Settings changed over the daemon's real API for dispatch only: `sandboxMemoryReserveMb=512`,
  `sandboxMemoryMb=2048` (F8).

## Could not run here

- **A full `linux-kvm` bring-up.** The guest image (`Granulefile`) build failed inside `docker
  build` at `curl https://download.docker.com/linux/debian/gpg`: "curl failed to verify the
  legitimacy of the server". This sandbox's package-cache DNS proxy intercepts that host under its
  own CA, which the build container does not trust. That is a property of this sandbox, not of
  grain, so no granule VM sandbox was booted.
- **"Docker missing" preflight**: docker-ce is part of this image. `linux-container` does not
  check docker in preflight by design; `ensureDocker` installs it later (`[manual]` without root).
- **A real clone and push**: there is no GitHub credential, and init cannot be pointed at githubsim.
- **Non-Linux refusal**: covered only by `TestInitRefusesNonLinuxAndRunsNoOtherStep`.

## Fixes

| Commit | Change |
|--------|--------|
| `fix(hostinit): let a re-run of grain init exit 0 once the deployment is up` | F1, F2, F3 |
| `fix(hostinit): stop a grain init re-run from deleting the admin password` | F4 |
| `fix(hostinit): resolve grain init's paths to absolute ones` | F5 |
| `fix(hostinit): point the default team at the GitHub credential init writes` | F6 |
| `docs(grain-init): fix the mismatches found by following docs/grain-init.md literally` | docs, `-help` |

Locally: `gofmt -l` is clean, `go vet ./pkg/hostinit`, `go test ./pkg/hostinit ./cmd/grain
./tests/deploy` pass, and `make security` passes (`go vet ./...`; `tests/security` ok). No
script was changed, so there was nothing new to shellcheck. `scripts/setup.sh` was checked anyway
and is clean.

## CI verdict

Full suite (`wait_for_checks` without `jobs`): grain CI run `9dc0bde56e8ede41` at `006f067` is
**success**. `frontend`, `go`, `installer`, `lint`, `package cache`, `postgres`, `terraform` and
`ui e2e` all passed. No known flake recurred. Later commits on this branch change only this report,
and their own run is the one the merge gate reads.
