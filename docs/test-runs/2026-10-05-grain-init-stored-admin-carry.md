# 2026-10-05: `grain init` carries a root deployment's stored admin to `-daemon-user=grain`

This walks the fix for finding H2 of
[`2026-10-05-grain-init-adopt-and-nonroot.md`](2026-10-05-grain-init-adopt-and-nonroot.md)
(task 2073): a `-data-dir` a root daemon seeded keeps `adminAccounts: ["root"]`, so a daemon that
`grain init` moves to the `grain` account acted as a non-admin.

## Environment

- Sandbox: Debian 12, systemd as PID 1, passwordless `sudo`, Docker from Docker's apt repository.
  No `grain` account existed beforehand.
- `grain init` and the hand-started root daemon were `/tmp/grain`, built with
  `go build -o /tmp/grain ./cmd/grain` from `grain/task-2073` at `3509dc7a4`. The daemon the unit
  runs is the `bin/grain` init's own build step produced from the same checkout.
- `C` below is `-platform=linux-container -repo-root=$PWD -install-claude-cli=false
  -data-dir=/var/lib/grain-old -ui-addr=127.0.0.1:8430 -daemon-arg=-ui-addr=127.0.0.1:8430`. No
  GitHub token was given.

## Steps

| # | Command | Exit | Result |
|---|---|---|---|
| S-1 | `sudo install -d -m 700 /var/lib/grain-old`; `sudo timeout 20 /tmp/grain daemon -data-dir /var/lib/grain-old -ui-addr 127.0.0.1:8430 -disable-native-sandboxes` (a root daemon started by hand) | 124 | While up, `GET /api/config`: `actor root`, `adminAccounts ["root"]`, `isAdmin true`. Everything under the data dir is `root:root`. |
| S-2 | A pre-feature unit at `/etc/systemd/system/grain-daemon.service` with no `User=`, `ExecStart=/tmp/grain daemon -data-dir /var/lib/grain-old -ui-addr 127.0.0.1:8430 -disable-native-sandboxes`; `systemctl start`, then `systemctl stop` | — | Ran as OS user `root`, `actor root`, `adminAccounts ["root"]`. Afterwards `is-active` is `inactive`. |
| S-3 | `sudo grain init C` | **10** | 56s. `[done]` create the daemon account (`created grain:grain`), docker group, sandbox network, build, setcap, `chown -R grain:grain /var/lib/grain-old`, start the daemon, API reachable. Then `[done] keep the stored admin as the daemon's actor`: "this -data-dir's stored admins are root … and the daemon now acts as "grain", which is not one -- added -as root to … grain-daemon.service and restarted it". `[done] created "root"'s password`. `[done] set the default team's worker slots: maxWorkers=1` (on H-5 this was a 403). The one `[manual]` is the sandbox budget, which it now reads: 3969 MiB of sandbox memory cannot hold one 8192 MiB sandbox on this 8 GB host, the same as H-7. |
| S-4 | State after S-3 | — | The unit has `User=grain` and its `ExecStart` ends `-ui-addr=127.0.0.1:8430 -as root`. The process runs as OS user `grain`. `/api/config`: `actor root`, `adminAccounts ["root"]`, `isAdmin true`. `find /var/lib/grain-old ! -user grain` is empty. |
| S-5 | `sudo grain init C` again | **10** | `[skipped] start the daemon: grain-daemon.service is already running, with the unit this run would have written`: the rewritten unit is recognized as init's own. No restart, and the stored-admin step does not appear (the actor is already an admin). `[skipped] provision the admin account's password: "root" already has an account`. `[skipped] worker slots: already 1`. Only the budget is `[manual]`. |

## The budget side observation (H-5 vs A-8)

`GET /api/settings` does not filter by caller. `sandboxCommitment` comes from
`Client.sandboxCommitment` (`pkg/ui/sandbox_commitment.go`), which is `nil` when the host cannot be
counted or when no team has any worker, merger or CI slots. On H-5 the worker-slot PUT had been
refused (403, the H2 actor), so no team had slots and the settings carried no commitment. A-8's
store already had slots from an earlier run. `checkSandboxBudget` now says so (`[skipped]`, naming
the failed worker-slot step when there is one) rather than "nothing bounds a run". With H2 fixed,
S-3 gets its worker slots and reads the budget.

## Stubs

None. No GitHub token was given, so the GitHub steps were skipped.
