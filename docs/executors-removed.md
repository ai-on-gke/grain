# Remote executors were removed

On 2026-11-01 the operator decided to remove remote executors from grain
outright, with no migration into hostd (task 2324). No deployment had
executors or executor tokens worth keeping. `grain hostd` is now grain's
only host agent: it listens on `/run/grain-hostd/hostd.sock` on the
daemon's own machine, and a hostd on another machine dials in to
`/api/hostd/connect` with the token the daemon was started with
(`grain daemon -hostd-token-file`, `grain hostd -server <url> -token-file`).

The removal took out:

- `grain executor` / `grain model-server` / `grain modelexec`
  (`pkg/cli/executor.go`) and the protocol behind it (`pkg/modelexec`).
  The IAP token provider `grain hostd -server -iap-audience` uses moved
  to `pkg/hostd/iap_auth.go`.
- The install scripts (`scripts/run-remote-executor.sh`,
  `scripts/run-model-server.sh` and their symlinks,
  `pkg/ui/executor_install.sh`) and the routes that served them and the
  grain binaries they downloaded (`/install-executor.sh`,
  `/api/executors/install.sh`, `/api/model-servers/install.sh`,
  `/api/executors/bin/{binary}`, `/install/{os}/{arch}`).
- The executor API: `/api/executors`, `/api/executors/tokens` (list,
  create, revoke, delete), and the connection routes `/_grain/executor`,
  `/api/executors/connect` and `/api/executors/stream`.
- The daemon's `-executor-addr`, `-executor-token-file`,
  `-executor-wait`, `-executor-cert-file` and `-executor-key-file` flags
  and their `-model-exec-*` aliases. A unit that still passes one starts
  anyway: the daemon ignores an unknown flag and reports it on its health
  page.
- Settings → Executors. `/settings/executors` now opens Models, where
  "Where each agent CLI runs" sets up grain hostd. System → Sandboxes'
  list of connected model servers is gone too.
- The `executor_token` and `executor_token_sequence` tables, which schema
  84 drops. A state-repository export that still carries them imports;
  the importer skips a table this build lacks.
- `grain mcpserver`'s `GRAIN_EXECUTOR_SOCK` tunnel client, and the
  GitHub-credential pins that guarded MCP servers arriving over the
  tunnel.
- The `grain model-server` unit `scripts/migrate-local-to-gke.sh`
  installed beside hostd. `--rollback` still removes a unit an earlier
  run left.

## What a remote hostd does and does not do yet

Before deleting anything, the removal checked each thing an executor did
against a remote hostd:

| Executor feature | Remote hostd today |
| --- | --- |
| Run an agent CLI (agy, claude, codex) on another machine | Yes. Host CLIs go through the hostd session (`remoteHostExecutor`, `pkg/daemon/hostexec.go`) |
| Host-pinned capability servers on that machine | Yes. `run: host` capability servers attach over the session |
| Stage the run's files (MCP config, agy's HOME and session store, CODEX_HOME) on the remote machine | **No.** `agent.HostdHost` stages on the daemon's machine |
| Serve the run's `grain mcpserver` back from the daemon over the connection (an MCP tunnel, admitted per run) | **No.** Hostd runs reach the run's MCP on the daemon's loopback |
| Carry the git proxy, and grain's own capability proxies, to the remote CLI | **No.** Both are loopback listeners on the daemon's machine |
| Use the remote host's own codex login | **No** |
| Several named remote hosts, each with its own token, and connections, concurrency and usage-limit pauses kept per host (`executor-<id>` connections) | **No.** One remote hostd at a time, with one shared token. This is the hostd registry, [Task #2164](/tasks/2164) |

So a remote hostd works today where the CLI can reach the daemon's
filesystem and loopback, for example a hostd on the same machine or
sharing its network namespace. It does not yet work for a CLI on a truly
separate machine. The follow-up filed from task 2324 adds that over the
hostd session. Until it lands, a deployment that needs agent CLIs on
another machine has no supported path.
