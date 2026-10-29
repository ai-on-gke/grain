# Remote host execution: grain hostd dialing out, the Settings notice, `grain model-server`, and the GKE migration dry runs

- **Plan:** `docs/test-plans/2026-10-29.md` §6 (PRs #97, #121, #103, #95, #111)
- **Date:** 2026-10-29, 14:17–15:20 UTC
- **Task:** [Task #1800](/tasks/1800), branch `grain/049f1e09/task-1800` on `ai-on-gke/grain`, base `main` at `d71aa7e4` ("Merge pull request #133 from ai-on-gke/grain/049f1e09/task-1752"); origin [Task #1753](/tasks/1753)
- **Environment:**
  - Debian 12 sandbox (`kubernetes-granule`), `linux/amd64`, running as uid 1000 (`debian`). systemd 252 (`252.39-1~deb12u2`) is PID 1.
  - Go `go1.26.10`, Node `v20.19.0`, Playwright Chromium `chromium-1234` from `/var/cache/playwright`.
  - `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`. This was rebuilt from this branch (`v0.0.0-20261029144146-b0ce949543c6`) for the browser walk.
  - No Gemini key, so `LOOK=1` was not used. Pages were checked with Playwright DOM and bounding-box reads, not by eye.
  - Scratch directory `/var/tmp/w6`. Ad-hoc walk scripts were kept outside the repository.

## Processes and exact commands

| Process | Command |
|---|---|
| Daemon (real, separate process) | `grain daemon -data-dir /var/tmp/w6/data -ui-addr 127.0.0.1:18090 -sandbox-port-base=0 -as alice -agy-path /bin/true -gemini-api-key-file /var/tmp/w6/gem.key -github-host 127.0.0.1:9 -github-insecure-http -host-sandboxes -sandbox-dir /var/tmp/w6/sb -hostd-token-file /var/tmp/w6/hostd.token` |
| hostd (real, separate process, over TCP) | `grain hostd -server http://127.0.0.1:18090 -token-file hostd.token -socket= -allowlist hostd.yaml -state-dir /var/tmp/w6/hstate` |
| hostd, wrong token | the same with `-token-file wrong.token` |
| Demo, both modes | `grain demo -addr 127.0.0.1:0 -open=false -access-code-file … -host-executor=hostd-remote` and `…=hostd-remote-down` (via `e2e/screenshot.mjs` and a Playwright walk) |
| Migration dry runs | the setup half of `scripts/migrate-local-to-gke_test.sh` (its stubbed `kubectl`, `systemctl`, `curl`, `docker`, `install`, and the `fresh` host layout), then `migrate --dry-run` and `migrate --rollback --dry-run` |

The hostd allowlist (`/var/tmp/w6/hostd.yaml`, `root:root 0644`, as hostd requires) offered `cli agy` (`/bin/true`) and `capability walk-mcp`. The capability was a 20-line Python MCP stdio server that answers `initialize` and `tools/list` and appends each method and its pid to `/var/tmp/w6/mcp-ran.log`. That log is how a host run was observed actually executing through the session.

The plan's shared environment passes `-sandbox-port-base=0` to `grain demo`. The demo rejects that flag (`flag provided but not defined: -sandbox-port-base`); only `grain daemon` takes it.

## Outcome

| # | Area | Outcome |
|---|---|---|
| 1 | Daemon + separate `grain hostd -server … -socket=` over TCP | **Pass.** Connected in <1 s. Settings status showed it reachable, its host, build, protocol 3, and offers. |
| 1 | A host run through the session | **Pass.** `POST /api/capability-servers/test` with `{"command":"walk-mcp","run":"host"}` answered `ok: true` in 31 ms. `initialize` and `tools/list` ran on the host under hostd. |
| 1 | Wrong token | **Pass.** `401 Unauthorized: grain hostd: this controller does not take that hostd token`. hostd keeps retrying with backoff, and nothing connects. The daemon logs nothing about the refusal (follow-up). |
| 1 | Daemon restart, hostd reconnect | **Fixed here.** hostd reconnected 15 s after a ~13 s outage and 18 s after a ~40 s outage, measured from when the daemon was serving again. Its 30 s backoff cap equalled the daemon's 30 s wait for hostd's hello. The cap is now 5 s. |
| 1 | hostd restart, notice down and up | **Pass.** Status went `connected: false` at once on kill and `connected: true, sessions: 2` within 1 s of restart. |
| 1 | Status while no hostd is connected | **Finding (follow-up).** `GET /api/settings/cli-run` takes 5.0 s. A `run: host` probe takes 30.0 s before it says none is connected. |
| 2 | `/settings/models`, `/system/sandboxes`, `/settings/executors`, both demo modes, 1440 and 390 px, light and dark | **Pass on layout.** 24 page loads with no page errors and 0 px horizontal overflow. The rename copy ("Model servers (grain model-server)") is on all three pages. |
| 2 | Notice when no hostd is connected | **Finding (follow-up).** It appears 5.6–5.7 s after load (0.5 s when connected). |
| 2 | Shown `grain hostd -server` command pasted | **Finding (follow-up).** Placeholders. Filled in, it fails on a host without the installer: no `/etc/grain/hostd.yaml`, then `mkdir /run/grain-hostd: permission denied` without `-socket=`. |
| 3 | `grain model-server`, `executor`, `modelexec` | **Pass.** All three print the same usage naming `model-server` with both aliases. `grain help` lists `model-server (aliases: executor, modelexec)`. |
| 3 | Install script URLs and symlinks | **Pass.** `/api/model-servers/install.sh`, `/api/executors/install.sh` and `/install-executor.sh` all return 200 with the same 28 951 B script (`md5 01b84ba1…`), which passes `bash -n` and prints its `--help`. `run-remote-executor.sh → run-model-server.sh`, and `bootstrap-executor.sh` and `grain-remote-executor.sh → run-remote-executor.sh`. |
| 4 | `migrate-local-to-gke.sh --dry-run` | **Fixed here.** The plan read as an operator would paste it had five `kubectl` lines with no `--kubeconfig`, a tar flag the real run does not pass, and a different marker command. |
| 4 | `--rollback --dry-run` | **Fixed here.** `starting pod/grain-data-transfer (, uid 1000)`: rollback never resolved the transfer image, so a real rollback applies `image: `. |
| 4 | Stage 2 against real systemd | **Fixed here.** `systemctl mask grain-daemon.service` fails on a setup.sh install, so a real migration died after stopping the daemon. |
| 5 | Clean-Debian `scripts/setup.sh` with `GRAIN_HOSTD=1` and no git | **Not run.** The sandbox has systemd as PID 1 and docker, but it is not a clean host: git is installed and the checkout needs it, and there was no disposable VM. `tests/deploy/state_repo_test.go` (`TestConfigureStateRepoUsesTheImageWhenTheHostLacksGit`) is the only coverage. The fallback was read: it applies when `GRAIN_HOSTD` resolves to `1`, git is absent, and `/usr/local/lib/grain/run-image.sh` is executable. |

## Steps and observations

### 1. Two real processes

- **Before any hostd.** `hostExecutor` was `kind: hostd, remote: true`, and `hostd.reachable: false` with the error "no grain hostd is connected to this daemon; on the host, run grain hostd -server with this daemon's URL and the token in its -hostd-token-file, and journalctl -u grain-hostd says why it is not connecting". The daemon's startup adoption waited the full 30 s for a hello (`14:22:56` host executor line, then `14:23:29` "grain hostd did not answer hello").
- **Wrong token.**

  ```
  hostd: the session to the controller ended (hostd: 127.0.0.1:18090 refused the connection (401 Unauthorized): grain hostd: this controller does not take that hostd token); reconnecting in 1s
  … reconnecting in 2s
  … reconnecting in 4s
  ```

  The daemon's log has no line for these refusals.
- **Right token.** `hostd: connected to the controller (hostd protocol 3, build v0.0.0-20261029134451-d71aa7e4378d)`, and on the daemon `grain hostd on debuerreotype (build …) connected from 127.0.0.1:34502`. Status: `reachable: true`, `offers: ["capability walk-mcp","cli agy"]`, `remote.connected: true`, `sessions: 1`, and `missing` listing grain's own allowlist entries (`host-git`, `grain-gchat`, `grain-gcp`, `grain-host-script`) with the entry text to add.
- **A host run.** `POST /api/capability-servers/test {"id":"https://walk.example/caps/walk-mcp","config":{"command":"walk-mcp","run":"host"}}` answered `{"ok":true,"methods":[]}` in 0.031 s. `mcp-ran.log` gained `initialize pid=4162` and `tools/list pid=4162`, and hostd logged `capability "walk-mcp" (pid 4162) ended` when the probe closed it. A bare `walk-mcp` id is refused ("must be an https:// URL"), so capability ids must be URLs even for an inline test.
- **Daemon restarts.** Measured from hostd's own log:

  | Outage | Daemon serving again | hostd attempts (s after drop) | Reconnected |
  |---|---|---|---|
  | ~13 s (killed 14:24:37, restarted 14:24:50) | 14:24:53 | 1, 3, 7, 15, then a 16 s wait | 14:25:08, **15 s after serving** |
  | ~40 s (killed ~14:26:06, restarted 14:26:46) | 14:26:49 | 1, 3, 7, 15, 31, then a 30 s wait | 14:27:07, **18 s after serving** |

  Both reconnects landed inside the daemon's 30 s `dialRetry` hello wait (adoption `14:25:08`, and `14:27:08` "0 adopted, 0 lost"). But a 30 s backoff cap leaves no margin against that window: a hostd that tries just before the daemon comes back can reconnect up to 30 s later. Settings says "not connected" for that whole time. Fixed below.
- **hostd restarts.** On `pkill` of hostd, the daemon logged `grain hostd on debuerreotype disconnected` and status went to `connected: false` with `since` updated. A restarted hostd connected in under 1 s (`sessions: 2`, new `addr`).
- **No hostd connected.** `GET /api/settings/cli-run` took 5.002 s and 5.003 s (`curl -w %{time_total}`), and the probe above took 30.002 s before failing with "run: host is not available here: … no grain hostd is connected". `Remote.Dial` returns `ErrRemoteNotConnected` at once, but `hostd.Client.dial` treats that as a restarting hostd and waits it out: 3 s (`hostdOffersTimeout`) plus 2 s (`hostdStatusTimeout`) for the settings read, and `dialRetry` for the probe. Filed as a follow-up, because the dispatch path shares that wait on purpose.

### 2. Browser

- `make ui-screenshots`' script (`node e2e/screenshot.mjs`, `BIN=/home/debian/bin/grain`, `DEMO_ARGS=-host-executor=<mode>`, `PAGES=/settings/models,/system/sandboxes,/settings/executors`) shot all 12 combinations per mode at desktop and phone, light and dark, into `/var/tmp/w6/shots/<mode>/` with a `contact-sheet.png` each, without page errors.
- A Playwright walk at 1440×900 and 390×844, light and dark, loaded each page in both modes (24 loads). `document.documentElement.scrollWidth - innerWidth` was 0 every time, and no `pageerror` fired.
- **hostd-remote.** The notice read "grain hostd is up on cloudtop, connected to this daemon (build v0.0.0-20261029144146-b0ce949543c6, hostd protocol 3), offering capability corp-search, cli agy. Host runs go through it." It appeared 477 ms (1440) and 481 ms (390) after navigation. agy's row says "host set by -agy-run: through grain hostd on the machine that dials in to this daemon, as its allowlist entry "agy"".
- **hostd-remote-down.** The notice read "This daemon takes a grain hostd that dials in to it, but none is connected. On the host, run `grain hostd -server <this daemon's URL> -token-file <the hostd token>`. A CLI set to host fails its runs until it connects." It appeared **5 753 ms** (1440) and **5 616 ms** (390) after navigation, so a 2.5 s look at the page shows no notice at all. The `<code>` wraps (`white-space: normal`) and sits inside the viewport at 390 px (x 75–328).
- **Pasting the command.** With the real values from §1 (`grain hostd -server http://127.0.0.1:18090 -token-file /var/tmp/w6/hostd.token`), it exits 1 with `lstat /etc/grain/hostd.yaml: no such file or directory`. With `-allowlist` added it exits 1 with `mkdir /run/grain-hostd: permission denied`. It works only with `-socket=` (or on a host whose installer made `/run/grain-hostd`). The URL placeholder is right to keep, because the browser's origin may be a proxy hostd cannot reach. Filed as a follow-up together with the second-hostd `-state-dir` hazard.
- **Rename.** `/settings/models` has "Model servers (grain model-server) run agent model CLIs (agy, claude, codex) on a remote host …". `/system/sandboxes` has "Model servers (grain model-server) connected now …". `/settings/executors` has "… remote model server worker daemons (grain model-server)". The section headings still say "executors" and "executor join tokens", and the empty state says "No executor tokens found". That is consistent with the route name and was left as it is.

### 3. `grain model-server`

`grain model-server --help`, `grain executor --help` and `grain modelexec --help` each exit 0 with `Usage: grain model-server [flags]` / `grain executor [flags] (alias)` / `grain modelexec [flags] (alias)`. The three install URLs serve one identical script, which names both URLs and both script names in its header.

### 4. Migration dry runs

`--dry-run` walked Stages 1–5b and exited 0. Read line by line, it had these problems:
- `kubectl -n grain create secret generic … | kubectl apply -f -`, `kubectl -n grain apply -f <pod/grain-data-transfer>`, `… | kubectl -n grain exec -i grain-data-transfer -- tar … --no-same-permissions` and `kubectl -n grain exec grain-data-transfer -- touch /data/.migrated-from-local` all lacked `--kubeconfig`. The real run uses `kc`, which passes it, so the plan an operator would paste targets the current context instead.
- The printed copy had `--no-same-permissions`, which the real copy does not pass (and should not, since it keeps `secrets.key`'s mode). The printed marker was `touch`; the real one is `sh -c 'date -u > …'`.
- `+ systemctl mask grain-daemon.service` comes right after `disable`. On this sandbox's real systemd 252, a unit file in `/etc/systemd/system` (where `setup.sh` installs it, `GRAIN_DAEMON_UNIT`) cannot be masked:

  ```
  $ sudo systemctl mask walk-mask.service
  Failed to mask unit: File /etc/systemd/system/walk-mask.service already exists.
  ```

  Under `set -euo pipefail`, a real migration therefore stops and disables the local daemon, pauses dispatch, and dies in Stage 2. Every re-run dies at the same line. The test's stub `systemctl mask` only touched a marker file.
- The controller image default `ghcr.io/bwsalmon/grain/grain:main` is the same default `scripts/setup.sh` uses (`GRAIN_IMAGE`), so it is consistent with the installer and not changed here.

`--rollback --dry-run` exited 0 but printed `starting pod/grain-data-transfer (, uid 1000)`. `rollback()` never calls `resolve_local_daemon`, so `TRANSFER_IMAGE` is empty and the pod manifest says `image: `, which a real API server refuses.

## Fixes in this branch

1. **`fix(migrate)`: `scripts/migrate-local-to-gke.sh`.**
   - Stage 2 moves `grain-daemon.service` to `grain-daemon.service.pre-gke` before `systemctl mask`. The unit readers (`unit_file`: image, sandbox image, `User=`) fall back to that copy.
   - `--rollback` moves the file back after `unmask` and reloads. It leaves the file and warns if a new unit was written in the meantime.
   - `--rollback` resolves the transfer image the way the migration does.
   - Every hand-printed dry-run line now matches what runs: `--kubeconfig` on each `kubectl`, no `--no-same-permissions`, and the real marker command.
   - `docs/grain-init-platforms.md` says so.
   - The sequence (mv aside → mask → start refused "Unit … is masked" → unmask → mv back → daemon-reload → `enable --now` active) was checked against the sandbox's real systemd with a throwaway unit.
   - Regression tests: the stub `systemctl` now masks the way systemd does (a refusal on the unit's own file, a `/dev/null` link otherwise), plus new cases for the unit moved aside and back, the rollback image, and `--kubeconfig` on every printed `kubectl`. The suite goes from 70 to 79 cases, and the unfixed script fails 14 of them.
2. **`fix(hostd)`: `pkg/hostd/remote_linux.go`.** `grain hostd -server`'s reconnect backoff is capped at 5 s (`defaultRemoteMaxBackoff`), well inside the daemon's 30 s `dialRetry`. `TestAHostdRedialsInsideTheControllersWaitForIt` asserts the margin and that redials stop growing at the cap; it fails on the old 30 s cap. `pkg/hostd` is not one of the trust-boundary paths (`scripts/setup.sh`, `pkg/runhost/*`), and neither of those was changed.

## Follow-ups proposed

- **Answer Settings → Models at once when no grain hostd is dialed in.** The 5 s cli-run read, the 5.6 s notice and the 30 s `run: host` probe while `Remote.Status().Connected` is false. Depends on task 1800.
- **Make the Settings "grain hostd -server" command work when pasted.** `-socket=` and `-allowlist` for a hand-run hostd, a drop-in for an installed one, the shared `-state-dir` hazard, and a daemon-side log line for refused hostd tokens. Depends on task 1800.

## Not exercised

- **Clean-Debian `scripts/setup.sh` with `GRAIN_HOSTD=1` and no host git** (plan item 5). There was no disposable clean host; see Outcome row 5.
- **`-iap-audience`, `-tls`, `-ca-file`, `-tls-server-name`.** There was no IAP or TLS front end in the sandbox. Only `http://` over loopback TCP was dialed.
- **A detached agent run across a daemon restart with a real agent CLI.** There was no model key, so adoption was seen only with 0 processes held. `TestADetachedRunOverTheSessionIsReattachedAfterTheControllerGoes` covers the reattachment.
