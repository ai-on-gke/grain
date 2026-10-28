# Host sandboxes: setup.sh and the "no isolation" alert (2026-10-16)

Task 4256, a follow-up to task 4192 and to task 4159, which brought
`-host-sandboxes` back to the daemon. It ran against `main` at
`c6d57de21` plus this branch. Before starting, `git log
-S'GRAIN_HOST_SANDBOXES' origin/main` showed only the removal
(`241229f0e`) and the commits before it, and `ui/src` had no host-sandbox
banner or alert. Neither had been restored.

§1 of `2026-10-02-sandbox-backends-addressing.md` recorded both pieces
being removed: `-host-sandboxes` ignored as an unknown flag, and `setup.sh`
refusing `GRAIN_HOST_SANDBOXES=1`. This run records them coming back.

## Result

No bugs found. The installer now refuses the same deployments the daemon
refuses, and the alert reaches everyone who opens the UI.

One design choice: the old standing banner (`HostSandboxesBanner.jsx`)
comes back as a row of the alerts panel (`src/alerts.js`, id
`host-sandboxes`, severity `warning`, scope Deployment), not as a strip
across the top of the page. Task 4008 moved every standing banner except
the disconnection banner into that panel. The bell turns warning-coloured
and counts the alert on every page, so the fact is still visible without
opening anything.

## 1. setup.sh (real script, as root, on a scratch sandbox)

`sudo env <vars> bash scripts/setup.sh`. Every refusal ran before anything
on the host was touched. Afterwards `id grain` reported no such user and
`/usr/local/bin/grain` did not exist.

| Variables | Exit | What it said |
|---|---|---|
| `GRAIN_HOST_SANDBOXES=1 GRAIN_UI_ADDR=0.0.0.0:4242` | 2 | `refusing GRAIN_HOST_SANDBOXES=1 on a deployment others can reach (GRAIN_UI_ADDR=0.0.0.0:4242)`, with the three ways out: a loopback `GRAIN_UI_ADDR`, an isolated backend, or `GRAIN_ALLOW_UNISOLATED_HOST_SANDBOXES=1` |
| `GRAIN_HOST_SANDBOXES=1 GRAIN_UI_ADDR=127.0.0.1:4242 GRAIN_IAP_RESOURCE=…` | 2 | the same refusal, naming `GRAIN_IAP_RESOURCE` |
| `GRAIN_HOST_SANDBOXES=1 GRAIN_CONTAINER_SANDBOXES=1` | 2 | `GRAIN_HOST_SANDBOXES=1 cannot be combined with another sandbox backend` |
| every backend `0` | 2 | `refusing to install without a sandbox backend`, which now lists `GRAIN_HOST_SANDBOXES=1` as the third answer |

`GRAIN_HOST_SANDBOX_ENV` (space-separated `NAME` or `NAME=VALUE`, one
`-host-sandbox-env` each) was added once task 4192's flag reached `main`
during this run. The same function refuses it without host sandboxes, for
`HOME`, and for a value systemd would rewrite in `ExecStart` (`$`, `%`,
`\`, quotes). The deploy test covers those cases too.

The accepted cases (a loopback address, or the allow variable) carry on
into a full install, which this sandbox cannot host. Instead,
`tests/deploy`'s `TestTheInstallerRefusesHostSandboxesWhereTheDaemonWould`
runs `check_host_sandboxes` itself over the addresses
`loopbackListenAddr` distinguishes (127.0.0.0/8, `localhost`, `[::1]`,
`0.0.0.0`, `:port`, `[::]`, a LAN address), with and without IAP and the
allow variable.

## 2. The daemon it installs (real `grain daemon`, scratch `-data-dir`s)

| Flags | Exit | What it said |
|---|---|---|
| `-ui-addr 0.0.0.0:0 -host-sandboxes -sandbox-dir …` | 2 | `-host-sandboxes runs every task unisolated, … reachable by others (-ui-addr 0.0.0.0:0)` |
| `-ui-addr 127.0.0.1:0 -host-sandboxes -sandbox-dir …` | runs (killed by `timeout 15`) | `host sandboxing -- one plain directory per run under …/runs, on this host, with NO isolation` |
| `-ui-addr 0.0.0.0:0 -host-sandboxes -allow-unisolated-host-sandboxes -sandbox-dir …` | runs | the same startup line |

These match the installer cases in §1.

## 3. The alert (Playwright, `ui/e2e/host-sandboxes-alert.spec.js`)

The spec boots a real `grain daemon` (`e2e/realdaemon.js`) and drives the
built frontend in Chromium. It does not use `grain demo`, because demo has
no running backend to report. The only stubs are the ones `realdaemon.js`
already uses: the agent CLI and `docker`. No task is dispatched.

- With `-host-sandboxes`, `/api/config` reports `hostSandboxes: true`, and
  the bell's `data-severity` is `warning` before the panel is opened. The
  panel then shows "No isolation: runs execute on this host", scoped to
  Deployment, with a sentence saying runs execute as the daemon's user on
  its machine and naming `-container-sandboxes`. A plain member (no admin
  role), signed in with a password and without Admin mode, gets the same
  `hostSandboxes: true` and sees the same row.
- With `-disable-native-sandboxes`, `hostSandboxes` is absent and the open
  panel has no such row.

Both tests pass (`npx playwright test e2e/host-sandboxes-alert.spec.js`,
2 passed). In this sandbox, downloading Playwright's browser needed
`NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
