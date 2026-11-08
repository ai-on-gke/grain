# §5 Walkthrough — hosts: `--join` pairing, `grain-agentd`, key-bound renewal, placement, ambient logins & deletion

- **Plan:** [`docs/test-plans/2026-11-07.md` §5](../test-plans/2026-11-07.md) (PRs #641, #719, #751, #754, #759, #764, #768, #832; schemas 111 and 117)
- **Date:** 2026-11-08, 02:00–02:50 UTC (attempt 2; attempt 1 had run out of quota before it wrote this report)
- **Task:** 3489 (origin task 3157), branch `grain/098073e4/task-3489-walkthrough-hosts-join-pairing`
- **Base tested:** `origin/main` at `46ab9012` (PR #876), with this branch's commits on top
- **Scripts:** [`2026-11-07-hosts-enrollment-placement/`](2026-11-07-hosts-enrollment-placement/)

## Environment

A Debian bookworm sandbox VM (linux/amd64, kernel 6.1, 7 vCPU), Go 1.26.10, Node 20.19. `ui` was built with `npm ci && npm run build`,
and `grain` and `grain-agentd` with `make grain grain-agentd BIN=/home/debian/bin` (release `v3.2.0-dev.2174+7e2df53`). Playwright's
Chromium was installed with `npx playwright install chromium`, with `NODE_EXTRA_CA_CERTS` set to the system CA bundle.

| Process | What it is |
|---|---|
| daemon `127.0.0.1:4280` | real `grain daemon` on SQLite ([`start-daemon.sh`](2026-11-07-hosts-enrollment-placement/start-daemon.sh)): `-host-sandboxes -sandbox-port-base=0 -as walkthrough-owner`, GitHub pointed at the stub below. Restarted several times during the walk, later with `-hostd-socket /tmp/host-a/hostd.sock` (so host CLIs start through a host agent, as in a container install) and `-upgrade-src-dir` (so `GET /api/upgrade` is live) |
| GitHub stub `127.0.0.1:4291` | [`fake-github.py`](2026-11-07-hosts-enrollment-placement/fake-github.py): the OAuth device flow and `/user`, so that `-join` can verify the person enrolling on loopback. The person is `github:walkthrough-owner` |
| `host-a`, `host-b` | `grain host -server … -join` with their own `HOME`s under `/tmp/host-*` ([`setup-host.sh`](2026-11-07-hosts-enrollment-placement/setup-host.sh), [`join-host.sh`](2026-11-07-hosts-enrollment-placement/join-host.sh), [`run-host.sh`](2026-11-07-hosts-enrollment-placement/run-host.sh)). Each has a root-owned allowlist offering a stub `claude` and a `walk-echo` capability. `host-a` also has `~/.claude/.credentials.json` and `~/.codex/auth.json` |
| `host-d` | rootless `grain-agentd -server … -join`: no root, and an allowlist that the user owns with mode `0600` |
| `host-e` | a legacy `grain host register` host with a per-host token, moved to a key-bound credential with `grain host rekey` |

The only thing simulated was GitHub. The enrollment codes, tokens and access codes shown below belong to throwaway local
servers, which no longer exist.

## Results

### 1. Live `--join` enrollment (`grain host` and `grain-agentd`)

| Check | Result |
|---|---|
| `grain host invite -name walk-a -labels pool=walk -capacity 2` | prints the code and the shared one-line snippet `curl -fsSL …/hostd/install \| sh -s -- --join ghe_… --release …` ✅. Settings → Hosts → Invite hosts renders the same snippet (screenshots, §5) ✅ |
| `grain host -join` on host-a | runs the GitHub device flow, then prints `Host`, `Enrolled by: github:walkthrough-owner` and `Key: SHA256:g0bM…`, and waits. `grain host pending` lists the request with the same enroller and the same key fingerprint ✅ |
| `grain host accept -name host-a <id>` | the host saves its credential (`0600`) next to its `0600` Ed25519 key, then connects. `grain host list` shows it key-bound, `cap 2 pool=walk` ✅ |
| `grain host reject <id>` (host-b) | the waiting host exits with `host join handshake ended: join request was rejected or expired` ✅ |
| a join with the code from before `rotate-code walk-a` | `401 unknown or rotated host enrollment code` ✅. Rotation also sets `uses` back to 0, as designed (`host_enroll.go`) |
| a join after `revoke-code walk-a` | `403 host enrollment code has been revoked` ✅ |
| a code limited with `-allow github:someone-else` | `403 principal "github:walkthrough-owner" is not permitted to use enrollment code "walk-other"` ✅ |
| misleading metadata ([`misleading-join.go`](2026-11-07-hosts-enrollment-placement/misleading-join.go)) | an ANSI escape in the hostname, a newline in `local_user`, a bidi override in `version`, an oversized `os` and a comma in a CLI name are each refused with a `400` that names the field. An honest control request gets the `401 github_device` challenge ✅ |
| "SAS pairing code vs verified-enroller auto-accept" | Since `bb42ab18` ("accept joining hosts on the verified enroller, without a pairing code") **no pairing code is shown on either side**. The owner checks the verified enroller and the key fingerprint, then accepts. The server still derives the code (`DerivePairingCode`) and refuses a mismatch. Even a code with `-allow github:walkthrough-owner` still waits for **Accept**, so nothing is accepted automatically. This matches the current design; the plan's wording predates it ✅ |
| `grain-agentd -check` with a `capabilities:` section | `names capabilities (walk-echo): grain-agentd runs agent CLIs only; remove them, or run `grain host`, which serves capabilities too` (the wording comes from this branch's earlier commit `cc83be49`) ✅ |
| `grain-agentd -join` (host-d, rootless) | same device flow, same pending card, then `Accepted as host "host-d"`. The key and credential are saved under `~/.config/grain/` (`0700` dir, `0600` files) and the agent connects ✅ |
| CLI logins reported in `GET /api/hostds` | `host-a: logins [claude, codex]`, `host-d: [claude]`, `host-b: none` ✅ |
| a capability server placed on host-d | refused: `the host agent's allowlist has no capabilities entry "walk-echo" (it offers no capabilities entries)` ✅. ⚠️ The hint that follows says to add the entry to `/etc/grain/hostd.yaml`. That is not where an agents-only host keeps its allowlist, and grain-agentd refuses such an entry anyway (follow-up filed) |

### 2. Ed25519 key-bound renewal, `rekey` and replay ([`renew-probe.go`](2026-11-07-hosts-enrollment-placement/renew-probe.go))

| Check | Result |
|---|---|
| `POST /api/hostd/renew` signed with a different key | `401 renew signature verification failed` ✅ |
| the host's own proof | `200` with a fresh token and `expires_at` 30 days out ✅ |
| the same proof sent again | `401 replayed renew signature nonce`: the replay cache that the HTTP route shares with the in-band `OpRenew` and `/api/hostd/connect` ✅ |
| a fresh proof on the credential that was just replaced | `200` within `hostdRenewGrace` (2 min), the window a renewal racing a reconnect needs; the superseded token is cut to that grace ✅ |
| renewal of a legacy per-host token (host-e) | `403 this host holds a legacy per-host token, which does not renew; migrate it with rekey` ✅ |
| **silent renewal of an old credential on restart** | host-a stopped, its token file backdated by 13 h (`touch -d '13 hours ago'`), host-a restarted. It renewed **as soon as it connected**: the token file was rewritten, `ghd_7120…` → `ghd_4925…`, and `token_expires_at` moved. This is the fix from `cc83be49` (below) working against the live daemon. Before it, renewal was a ticker started fresh on every connection, 12 h after it opened, so a host that reconnects more often than every 12 h never renewed ✅ |
| daemon restart under connected hosts | host-a, host-b, host-d and host-e all reconnect within 3 s with the same credentials. Nothing renews again, because the schedule now survives a reconnect ✅ |
| `grain host rekey -token-file … -key-file … host-e` | files a `rekey:host-e` join request. After `accept`, the key and the key-bound credential are saved, the old legacy token is refused (`401`), and the host reconnects key-bound ✅ |
| reconnecting with the right token but the wrong key | `401 host agent: host signature verification failed`, retried with backoff ✅ |
| bounded replay cache | not exercised live (it would take tens of thousands of nonces). The unit tests in `pkg/hostd` cover it, and they pass |

### 3. Connection and capability-server placement, ambient credentials

| Check | Result |
|---|---|
| ambient connection with no host, development daemon (bare machine, host executor `local`) | accepted. This is by design: the CLI is the daemon's own child, so there is only one login it can mean (`docs/host-placement.md`) ✅ |
| same with `-hostd-socket` and claude set to `host` (`PUT /api/settings/cli-run`) | a **new** ambient connection with no host is refused with `ambient credentials are the agent CLI's own login on the machine it runs on … choose the host it runs on (Run on host) first` ✅. With `hostd: host-a` it saves ✅. Stored credentials with no host save ✅. A connection saved ambient and unplaced before the rule is left alone ✅ |
| auto-filling the only host, and the logins shown in the picker | the API refuses an ambient connection with no host however many hosts there are. The Settings form fills in the host when there is exactly one, and lists each host's logins: `connection-credentials.spec.js` passes against the demo (§5) ✅. The live Settings → Models page renders without overflow ✅ |
| capability server placed by name (`hostd: host-b`) and by label (`{"labels":{"role":"caps"}}`, after `grain host update -labels pool=walk,role=caps host-b`) | both save and attach on host-b. They then report `failed: initialize: context deadline exceeded` because the stub entry is `/bin/cat`, not an MCP server; this was expected ✅ |
| host-b taken offline | both servers turn to `needs-hostd:host-b` / `needs-hostd:role=caps` with `waiting for host host-b` ✅. They come back when host-b reconnects ✅ |
| `GET /api/upgrade` while a host is offline (#719) | about 1 ms with host-b offline. With upgrades on and the host agent the daemon starts CLIs through **hung** (`SIGSTOP`, socket still there), the first poll takes **3.0 s** (`hostdOffersTimeout`) and the following polls take about 1.2 ms. After the 15 s cache of failed probes runs out, one more poll pays 3.0 s. Once the agent resumes, polls are about 2 ms. That is #719's design: one probe per 15 s instead of 5 s on every poll. ⚠️ The plan's "< 200 ms" holds for every poll except the one that refreshes the cache (follow-up filed) |

### 4. Revoke, delete and `Delete all revoked`

| Check | Result |
|---|---|
| task attempt on a connection placed on host-b (`noRepo`, `agentBackendId: claude-on-b`) | `grain get 2`: `#1 … failed, on walk-model (claude), host host-b` ✅. The run failed only because the stub allowlist does not pass `MCP_TOOL_TIMEOUT`, which does not matter for this check. ⚠️ That failure reason reads `hostd refused: cli "claude": …`, so the old word reaches a task's `last failure` line (follow-up filed) |
| `grain host delete host-b` while it is live | `host host-b is not revoked: revoke it first, which disconnects it, then delete it` ✅ |
| `grain host revoke host-b` | the connected agent is cut off at once (`401 this controller does not take that host token`) ✅ |
| delete while a connection and a server still name it | `host host-b is still named by model connection "claude-on-b" (hostd), capability server "https://walk.example/caps/echo-b" (hostd): point them at another host …, then delete it` ✅. A server placed by label is not counted, since no host name is stored on it |
| **`grain host delete-revoked`** | ❌ **Bug, fixed here:** it printed `grain host: lstat /etc/grain/hostd.yaml: no such file or directory` and deleted nothing. Details under Fixes |
| `delete-revoked` with the fix, host-b still named | `skipped host host-b: … still named by …` ✅. Once those references were removed and host-e revoked: `deleted host host-b from the registry`, `deleted host host-e from the registry` ✅ |
| history after deletion | `grain get 2`: `#1 … host host-b (deleted)`, `#2 … host host-b (deleted)`. In `-json` the attempt's `hostd` object keeps `hostd/instance/owner/at` and adds `deleted: true`. No foreign-key or nil errors ✅ |
| Settings → Hosts `Delete` / `Delete all revoked` | `hostds-delete.spec.js` passes (desktop and phone, with a confirmation before removing). The live page with a revoked `host-old` renders without overflow ✅ |

### 5. Playwright: layout and wording

- **Specs against the demo:** `host-wording`, `hostd-placement`, `hostd-install-instructions`, `hostds-delete`, `connection-credentials` and `hostds-settings-layout` all pass (16 of 16, 33.6 s). They cover install instructions supplied by the build (`GET /api/hostds/{name}/install`, wrapping at 1440 and 390 px), the Delete actions, the ambient picker's host and logins, and Accept moving a pending host into the table.
- **Live daemon** ([`shoot.mjs`](2026-11-07-hosts-enrollment-placement/shoot.mjs)): Settings → Hosts (with a pending join, a revoked host and three connected hosts), Settings → Models (five connections, ambient and stored) and Settings → Capabilities (the placed servers). Each was rendered at 1440×900 and 390×844 in light and dark: 12 of 12 have no horizontal overflow and no use of "hostd" as a word. The only matches were paths (`/hostd/install`, `hostd.sock`, `/etc/grain/hostd.yaml`). There were no UI changes, so this report has no contact sheet.
- **Install instructions:** `GET /api/hostds/host-a/install` returns `{"release": …, "lines": ["sudo sh -c \"$(curl -fsSL '…/api/hostd/install.sh')\" grain-hostd-install --name host-a --release …"]}` ✅.
- **CLI wording:** `TestEveryCommandsHelpSaysHostNotHostd`, the `CLISpec` tests and `grain-agentd`'s `TestAgentdSaysHostNotHostd` pass. Since `cc83be49`, `grain-agentd -h` says "host agent" throughout.

## Fixes on this branch

1. **A host's silent renewal schedule survives reconnects** (`cc83be49`, attempt 1). The renewal is now due `RenewInterval` after the credential was last issued, not after the connection opened. `grain host` and `grain-agentd` seed the schedule from the token file's modification time (`hostd.TokenFileIssuedAt`). The regression tests are `TestSilentRenewalSurvivesReconnectsShorterThanTheInterval` and `TestSilentRenewalIsDueFromWhenTheCredentialWasIssued`. The same commit changes `grain-agentd`'s `-h`, its `install -h`, the generated allowlist header and the token prompt to say host / host agent / `grain host` (`TestAgentdSaysHostNotHostd`).
2. **`grain host delete-revoked` (and `grain host rm`) reach the host registry.** `hostRegistryVerb`, which decides whether `grain host <word>` is a registry command or the start of a host agent, did not list `delete-revoked` or `rm`. Both were documented and handled by `cmdHostds`, but they started the host agent instead, which on a workstation fails with `lstat /etc/grain/hostd.yaml`. `TestEveryHostSubcommandReachesTheRegistry` reads every subcommand from `hostRegistryUsage` and every case label from `cmdHostds` (via `go/parser`) and requires each to be a registry verb. It fails without the fix (on `delete-revoked` and `rm`) and passes with it.
3. **License headers** on the walkthrough's helper scripts. `tests/hygiene` `TestLicenseHeaders` was the `go unit` failure that sent this branch back from the merge queue.

## CI

- `go unit` had failed on `tests/hygiene` `TestLicenseHeaders` (the four helper scripts that attempt 1 committed). The cause was this branch's own change, not the base; fix 3 above.
- I ran `go unit`'s full package list locally with `-race`. The only other failure was `pkg/daemon` `TestAdoptedCISandboxFetchesAfterCARotationAcrossRestart` (`step runner a1-s0 missing in sandbox`). It failed while a UI build and a Go build were loading the VM at the same time, and passed 3 of 3 runs in isolation (`-race -count=3`). The branch does not touch it. This run had no access to GitHub CI history (`grain ci` is not granted), so whether it also flakes on `main` is unconfirmed.
- Merge-queue repair run (2026-11-08, ~03:00 UTC). At `55c6c34c`, `go unit` passed. Three other jobs failed:
  - `run host`: `TestDaemonRestartKeepsAWarmSessionSandboxOnlyUnderItsRecordedKeys` failed, and the log tail did not include an assertion.
  - `go e2e`: a test in `./tests/e2e/...`. The tail grain served did not name it.
  - `installer`: a subtest of `TestGoogleTrackRunsAgyThroughHostdAndMigratesFromHostMode`.

  None of these tests runs the code this branch changes. Before the commit that changed it, nothing in `tests/` referred to `RenewInterval`, `RenewedAt`, `hostRegistryVerb` or `delete-revoked`. Locally:
  - The three `run host` restart acceptance tests pass with `GRAIN_RUNHOST_DOCKER=1` (222 s).
  - `go test -race ./tests/e2e/... ./tests/deploy/...` passes (628 s and 368 s).
  - `go test -race` passes on `./pkg/agentd/... ./pkg/hostd/... ./pkg/cli/... ./tests/hygiene/... ./cmd/grain-agentd/...`.

  A clean local run of the installer suite was not possible. That suite installs into the VM it runs on, under `/etc/grain` and systemd. The first local attempt ran under `sudo` without `/usr/sbin` on `PATH`, so it could not find `runuser`, and the timeout cut it off before it could clean up. The second attempt then started from the leftover `/etc/grain/hostd.yaml` (`mapping key "fakecap" already defined`). `origin/main` was merged in (`d7dde0c2`, PR #877) and CI ran again.

## Follow-ups

- When a capability server is placed on a host that offers no capabilities entries (for example an agents-only `grain-agentd`), the refusal should not say to add the entry to `/etc/grain/hostd.yaml`. The task-failure prefix `hostd refused:` should say host / host agent.
- `GET /api/upgrade` behind a hung host agent: the poll that refreshes #719's 15 s cache of failed probes still waits the full 3 s. It could serve the cached failure while a background probe refreshes it.
