# `grain` CLI specification

**Status:** draft for review. Normative.
**Describes:** the code as it is today: the host/operator CLI (`cmd/grain`, `pkg/cli`), the in-sandbox CLI (`pkg/sandboxcli`, also served by `cmd/grain-sandbox`), and the connection code they share (`pkg/ui/httpclient.go`, `pkg/ui/localtoken.go`).
**Parts:**
- this file covers the rules every command follows: how the binary picks a CLI, configuration files, connection and auth, output, exit codes, help conventions, the coverage guard, and divergences from the designs;
- [`host.spec.md`](host.spec.md) covers every host/operator command and flag;
- [`sandbox.spec.md`](sandbox.spec.md) covers every in-sandbox command and flag, proxy discovery, and the namespaces.

**Related:** [`external-cli.spec.md`](../external-cli/external-cli.spec.md) specifies the external-agent *workflow* and API contracts (contexts, `login`, `whoami`, external tokens). For command syntax it points here: this spec is the **single source of truth for command and flag syntax**, and `external-cli.spec.md` does not restate it. Where `external-cli.spec.md` requires a command this binary does not register yet, [§8](#8-divergences) lists the gap.

The key words **MUST**, **MUST NOT**, **SHOULD** and **MAY** are to be read as in RFC 2119. Each requirement says what the code does now, and under **Pinned by** names the test that holds it, or says *untested*.

---

## 1. One binary, two CLIs

### 1.1 Dispatch order {#CLI-1}

`cmd/grain` runs `cli.Main` (`pkg/cli/main.go`), which **MUST** decide what to do with `argv[1:]` in this order:

1. When `sandboxcli.Main` handles the arguments, it runs the in-sandbox command and exits with that command's status. It handles them when `argv[1]` is a sandbox command ([`sandbox.spec.md` §1](sandbox.spec.md#1-commands-and-proxy-discovery)) **and** a sandbox proxy is detected ([#CLI-80](sandbox.spec.md#CLI-80)).
2. When `argv[1]` names a *mode* (`daemon`, `demo`, `mcpserver`, `runhost`, `hostd`, `host-script`, `access-code`, `secrets`, `connections`, `backup`, `export`, `import`, `state`, `controller`, `setup`, `init`, `sync`, `repo-case`, `version`, `schema-version`, `release-notes`, `sandbox-image`, `container-sandbox-image`, `k8s-sandbox-image`, `image`, `db`, `upgrade-postgres`, `upgrade-sqlite-to-postgres`, `agy-tool-hook`, `claude-stop-hook`, `codex-detached`), that mode runs with the rest of `argv` and its own flag set. `capability` is a mode only when its first argument names an in-tree capability server and the line is not the 3-argument `attach`/`detach` shape (`servesCapability`).
3. With no arguments inside a sandbox, the sandbox CLI's help is printed to stderr and the exit status is 2.
4. Otherwise the arguments go to the *task CLI* (`runCLI`): global flags, then one task command ([`host.spec.md` §2](host.spec.md#2-task-cli-global-flags-and-connection)). An unknown task command inside a sandbox gets the sandbox help with status 2 (`grain: unknown command "<x>"`). Outside a sandbox it gets the host usage on stderr, then `grain: unknown command "<x>"`, with status 1.

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestIsSandboxCommandLeavesOtherModesAlone`, `TestSandboxCLIOutsideASandboxIsNotHandled`; `pkg/sandboxcli/flags_test.go`: `TestUnknownCommandInASandboxGetsTheSandboxHelp`; `pkg/cli/capability_serve_test.go` (the `servesCapability` shape).

### 1.2 `cmd/grain-sandbox` {#CLI-2}

The sandbox image's own `/usr/local/bin/grain` (`cmd/grain-sandbox`) **MUST** answer:
- `host-script …` via `pkg/hostscriptcli`;
- every sandbox command the way `cmd/grain` does;
- `help`, `-h`, `--help` or no arguments outside a sandbox with its short usage on stdout and status 0;
- a sandbox command with no proxy detected with `grain: "<cmd>" answers over a sandbox's scoped proxy, and this process found none (no proxy credential in ~/.git-credentials, and GRAIN_SANDBOX_PROXY is unset)` and status 2;
- anything else with `grain: "<cmd>" is not a command this sandbox's grain has` followed by the usage, and status 2.

It carries none of the host CLI's commands.

**Pinned by:** `cmd/grain-sandbox/main_test.go`: `TestSandboxGrainAnswersTheSandboxCLI`, `TestSandboxGrainOutsideASandbox`, `TestUsageNamesTheSandboxCLI`.

---

## 2. Configuration files and environment

### 2.1 `/etc/grain/cli.env` {#CLI-3}

1. The host CLI **MUST** read `/etc/grain/cli.env`, or the file `$GRAIN_CLI_ENV` names when that variable is set. Setting `GRAIN_CLI_ENV` to an empty value turns the file off.
2. A missing or unreadable file **MUST** be treated as empty and **MUST NOT** cause an error.
3. The file **MUST** be parsed as data, never sourced:
   - one `KEY=VALUE` per line;
   - blank lines and `#` comments are skipped;
   - an optional `export ` prefix is stripped;
   - one matching pair of `"` or `'` quotes around the value is stripped.
4. Only `GRAIN_SERVER` and `GRAIN_DATA_DIR` mean anything. Other keys are ignored.
5. The environment variable of the same name **MUST** win over the file. A variable that is set but empty (after trimming) counts as unset.
6. Before any task-CLI call, and in `grain sync`, `GRAIN_DATA_DIR` from the file **MUST** be exported into the process when the environment lacks it (`useCLIEnvDataDir`). That is how the local API token ([#CLI-6](#CLI-6)) is found.

**Pinned by:** `pkg/cli/cliconn_test.go`: `TestTheCLIFindsItsDaemonInCLIEnv`, `TestCLIEnvIsReadAsData`; `pkg/cli/sync_test.go`: `TestCmdSyncPresentsTheLocalAPITokenFromCLIEnvsDataDir`.

### 2.2 Server resolution {#CLI-4}

The task CLI's target daemon **MUST** be:
1. `-server`, when given;
2. else `$GRAIN_SERVER`;
3. else the `server` of the selected context, when it has one;
4. else `GRAIN_SERVER` in the CLI env file;
5. else `http://127.0.0.1:4242`.

The context is `-context`, else `$GRAIN_CONTEXT`, else `current` in `$XDG_CONFIG_HOME/grain/config.json` (else `~/.config/grain/config.json`). A missing `config.json` means no context. This is [`external-cli.spec.md` #XC-2 and #XC-3](../external-cli/external-cli.spec.md#XC-3), and the commands that manage contexts are [`host.spec.md` #CLI-108](host.spec.md#CLI-108).

The source that won is remembered and reported in connection errors ([#CLI-7](#CLI-7)). `-server` is only a global flag of the task CLI; mode commands that talk to a daemon (`sync`, `demo`'s clients, `mcpserver -server`) take their own flags listed in `host.spec.md`.

**Pinned by:** `pkg/cli/cliconn_test.go`: `TestTheCLIFindsItsDaemonInCLIEnv`, `TestContextCommandsAndServerPrecedence`, `TestARefusedConnectionFailsFastAndSaysWhereTheDaemonIs`.

### 2.3 Data directory {#CLI-5}

Commands that edit a colocated deployment's files take `-data-dir`. These are `state`, `secrets`, `connections`, `backup`, `export`, `import`, `repo-case`, `upgrade-postgres`, `access-code` and `init workstation`. In most of them `-data-dir` **MUST** default to `$GRAIN_DATA_DIR`, else `GRAIN_DATA_DIR` in the CLI env file, else empty. When the command needs a data directory, empty is refused with `grain <cmd>: -data-dir is required`.

The exceptions:
- `access-code` resolves the token file itself ([`host.spec.md` #CLI-41](host.spec.md#CLI-41));
- `init workstation` defaults to `$GRAIN_DATA_DIR`, else `~/.grain`;
- `daemon`, `init` (linux platforms) and `controller bootstrap-github-app` have no environment default.

**Pinned by:** `pkg/cli/state_check_test.go`: `TestStateCheckNeedsNoDataDir`; `pkg/cli/init_test.go`: `TestCmdInitRequiresDataDir`; others *untested*.

---

## 3. Connection and authentication (task CLI and `grain sync`)

### 3.1 Credential order {#CLI-6}

`ui.HTTPClient` **MUST** pick at most one bearer credential per request, in this order:

1. `-token` (`HTTPClient.Token`);
2. a programmatic `TokenProvider` (none is set by `cmd/grain` today);
3. the first non-empty of `$GRAIN_TOKEN`, `$GRAIN_AUTH_TOKEN`, `$GRAIN_ID_TOKEN`, `$GRAIN_TUNNEL_TOKEN`;
4. for a loopback server only, the **local API token**, which is the daemon's **access code**. It is read from `$GRAIN_LOCAL_API_TOKEN_FILE`, else `$GRAIN_DATA_DIR/local-api-token`, and silently skipped when it cannot be read;
5. a Google ID token, when `-audience` (`$GRAIN_AUDIENCE`, else `$GRAIN_TUNNEL_AUDIENCE`) is set or the server is `https://` or `*.run.app`. The token comes from Application Default Credentials for the audience (default: the server URL), else `gcloud auth print-identity-token --audiences=<aud>`, else, for `*.run.app` servers only, a plain `gcloud auth print-identity-token`. It is cached until a minute before it expires.

A chosen credential **MUST** be sent with any leading `Bearer ` stripped, as both `Authorization: Bearer <tok>` and `X-Grain-Tunnel-Token: <tok>`. `-as` (`$GRAIN_USER`) **MUST** be sent as `X-Forwarded-User`. Every request carries `User-Agent: grain-cli` unless one is already set.

A scoped external agent token (`gma_…`) is not special-cased by the CLI. Passed as `-token` or `$GRAIN_TOKEN`, it is sent like any other bearer. The daemon authenticates it as `external:<name>`, held to its own scopes (`pkg/ui/server.go`, `pkg/ui/external_agents.go`). In **IAP mode** (`grain daemon -ui-require-iap`), the CLI reaches the daemon through the IAP proxy with a Google ID token minted for the IAP audience (`-audience`). Loopback break-glass is specified in [`authentication.spec.md` §5](../authentication/authentication.spec.md).

**Pinned by:** `pkg/cli/cli_flags_test.go`: `TestRunCLI_GlobalFlags`, `TestRunCLI_EnvVarDefaults`, `TestAudienceDefault_EnvVars`, `TestAsUserDefault_EnvVars`; `pkg/ui/httpclient_auth_test.go`.

### 3.2 Timeouts, retry and connection errors {#CLI-7}

1. `-connect-timeout` (default `5s`) **MUST** bound dialling and the TLS handshake. It **MUST** also be the total budget for retrying a refused connection, with 200ms initial and 1s maximum backoff.
2. `-timeout` (default `2m`) **MUST** bound only the wait for response headers. Once one call times out, every later call of the same command **MUST** fail with the same error without being sent (`stickyTimeout`).
3. `explainDaemonError` **MUST** rewrite:
   - a refused connection or DNS failure into `no grain daemon answered at <url>[ (the default)| (from <source>)]<hint>. Set GRAIN_SERVER or pass -server (<root cause>)`;
   - a timeout into `the grain daemon at <where> did not answer in time (-connect-timeout <d>, -timeout <d>)<hint>. Set GRAIN_SERVER or pass -server, or pass a longer -timeout for a slow call (<root cause>)`;
   - a 401, or a 403 saying `does not have access to this server`, into `the grain daemon at <where> refused this CLI's credentials (<daemon's message>): <credential hint>`. The credential hint follows the credential source: `the token passed with -token was refused` when `-token` was given; `the token from $<VAR> was refused` when `$GRAIN_TOKEN` (or `$GRAIN_AUTH_TOKEN`, `$GRAIN_ID_TOKEN`, `$GRAIN_TUNNEL_TOKEN`) supplied it; for a loopback server with no explicit token, whether the local API token is absent, unreadable (naming the account to `sudo -u` as), or stale; and for a non-loopback server with no explicit token, `no -token was given; pass a token with -token or set GRAIN_TOKEN` (no local-token advice).

   `<hint>` is `; this host's daemon listens on <url> per <cli.env path>` when the CLI env file names a different server.
4. Any other error, including every structured error the daemon returns, **MUST** pass through unchanged.

**Pinned by:** `pkg/cli/cliconn_test.go`: `TestARefusedConnectionFailsFastAndSaysWhereTheDaemonIs`, `TestAnUnansweredCallTimesOut`, `TestATimeoutNamesTheHostDaemonAfterTheServerThatTimedOut`, `TestAnUnauthorisedCallNamesTheUnreadableToken`, `TestAnUnauthorisedCallNamesTheCredentialSource`, `TestADaemonErrorIsNotRewritten`; `pkg/cli/list_unavailable_test.go`: `TestListStopsAtTheFirstCallTheDaemonDidNotAnswer`.

---

## 4. Output

### 4.1 Human and JSON output {#CLI-8}

1. Task commands **MUST** print human-readable text by default. With the global `-json`, a command that has a JSON shape **MUST** print the API response object it got, encoded with two-space indentation (`printer.encode`), instead of the table. Notes meant for a person (`printer.note`) are suppressed under `-json`. A command whose shape is listed in `host.spec.md` documents its schema there; otherwise the JSON is the matching `pkg/ui` response type.
2. Mode commands that offer JSON take their own `-json` flag (`version`, `release-notes`, `declarations check`, `sandbox-image check|contract`, `pause`).
3. In-sandbox commands **MUST** stream the proxy's response body to stdout as it comes. They **MUST NOT** cap or truncate it. Their `--json` flags (where listed) select the proxy's JSON form instead of its text form.

**Pinned by:** `pkg/cli/cicmd_test.go`: `TestPrintCIRunsJSON`; `pkg/cli/pause_test.go`: `TestPrintAgentPauseJSON`; `pkg/cli/failures_test.go`: `TestPrintFailuresJSON`; `pkg/cli/declcmd_test.go`: `TestDeclarationsCheckJSONCarriesTheVerdict`; sandbox streaming by `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIAgainstTheProxy`.

### 4.2 Output size {#CLI-9}

1. The host CLI **MUST NOT** truncate any output.
2. The sandbox CLI **MUST NOT** truncate output either. Size control is the caller's:
   - `--tail N`, `--offset N`, `--limit N` are byte ranges the namespace applies;
   - `--lines N` / `-n N` keeps the last N lines on the client side;
   - `grain task transcript` reports the full size on stderr (`grain: <uri> is <n> bytes; --tail, --offset and --limit take bytes, --lines N the last N lines`).
3. The only server-side caps are the namespace's own: the grep walk cap and the `[truncated: …]` line on a cut-short `ls` walk ([`sandbox.spec.md` #CLI-84](sandbox.spec.md#CLI-84)).

**Pinned by:** `pkg/sandboxcli/flags_test.go`: `TestReadTakesBytesAndLines`, `TestTaskTranscriptSaysHowBigItIs`, `TestTailWriterKeepsTheLastLinesAcrossWrites`.

---

## 5. Exit codes {#CLI-10}

| CLI | Status | Meaning |
|---|---|---|
| task CLI (`runCLI`) | 0 | success (and `-h` / `--help`) |
| task CLI | 1–5 | classified exit codes per [external-cli #XC-13](../external-cli/external-cli.spec.md#XC-13): `1` target failed, unclassified `5xx`/daemon error or `429` (`rate_limited`), `2` CLI usage error / unknown flag / `400`/`404`/`409`/`422`, `3` authentication or authorization failure (`401`/`403` / login redirect), `4` daemon unreachable (DNS, refused/reset TCP, TLS mismatch, HTTP timeout), `5` blocking wait (`wait`, `ci wait`) timed out; printed as `grain: <message>` on stderr in text mode or `{"error": ...}` under `-json` |
| mode commands | 0 / 1 | each mode exits 1 on error after printing `grain <mode>: <message>`. Exceptions are listed per command in `host.spec.md`: `init` has three outcomes, `backup -h` and `access-code -h` exit 0, `release-notes` given stray arguments exits 2 (`grain release-notes: unexpected arguments: …`), and `version`, `schema-version` and the image printers exit 0 (they ignore stray arguments) |
| sandbox CLI | 0 | success |
| sandbox CLI | 1 | an error. Either the proxy refused (its body printed as `grain: <body>`), the transport failed (`grain: reaching the sandbox proxy at <base>: <err>`), or a verdict command reported failure (`ci wait`/`run-job` on a failed run; `grep` on a truncated walk that matched nothing) |
| sandbox CLI | 2 | a usage error, printed as given with no `grain:` prefix: an unknown command, subcommand or flag, a missing positional argument, or one of the malformed counts the parser checks itself (`grep -C`/`--max`, `read --page`, `log` counts). Value checks made later by the command are status-1 errors with the `grain:` prefix instead: `task list --limit 0`, `ci wait`/`run-job --timeout`, `pr --comment-id`/`--line`, `task propose --priority`/`--custom-field`/`--not-before`/missing `--body`, `git revert` without exactly one of `--task`/`--commit` |

**Pinned by:** `pkg/sandboxcli/flags_test.go`: `TestMissingArgumentIsAUsageError`, `TestUnknownSubcommandGetsTheSandboxHelp`; `pkg/sandboxcli/flags_test.go`: `TestSandboxCommandsRefuseFlagsTheyDoNotRead`; `pkg/cli/init_test.go`: `TestInitExitCodeDistinguishesTheThreeOutcomes`; `pkg/cli/backup_test.go`: `TestBackupHelpExitsZero`; `pkg/sandboxcli/actions_test.go`: `TestSandboxCIWaitReportsEachVerdict`.

---

## 6. Flag syntax and help conventions

### 6.1 Host flags {#CLI-11}

1. Host commands **MUST** parse flags with Go's `flag` package. That means:
   - `-name value`, `-name=value` and `--name` are all accepted;
   - a bool takes `-name` or `-name=false`;
   - parsing stops at the first non-flag argument.
2. To accept `grain <verb> <id> [flags]`, task commands reorder arguments (`parseArgs`) so flags after the id are parsed. Exception: `comment` keeps everything after the id as its body. A flag written after the id **MUST** be refused with a message naming the flag (`misplacedFlag`).
3. A task command given positional arguments it has no use for **MUST** refuse them (`atMostArgs`).
4. Global flags (`-url`, `-token`, `-token-file`, `-context`, `-as`, `-json`) are accepted either before or after the command (`liftGlobalFlags`, [external-cli #XC-11](../external-cli/external-cli.spec.md#XC-11)), except after `comment <id>`, where every remaining token belongs to the comment body.

**Pinned by:** `pkg/cli/args_test.go`: `TestCommandsRefuseArgumentsTheyHaveNoUseFor`, `TestHostLocalCommandsRefuseArgumentsTheyHaveNoUseFor`, `TestArgumentCheckWording`; `pkg/cli/comment_flag_order_test.go`: `TestCommentRefusesAFlagWrittenAfterTheTaskID`, `TestMisplacedFlagOnlyMatchesThisCommandsOwnFlags`.

### 6.2 Host help {#CLI-12}

1. `grain <command> -h` **MUST** print the command's usage to stderr. For most commands that is `flag.PrintDefaults` output, sometimes after a hand-written paragraph.
2. Commands whose usage is hand-written: `access-code`, `secrets`, `state`, `connections`, `import`, `runhost`, `host-script`, `controller`, and the global usage. For these the flags are listed in `host.spec.md` prose next to the generated table, because `-h` does not print them in `PrintDefaults` form.
3. `grain` with no arguments outside a sandbox **MUST** print the global usage (`pkg/cli/main.go`'s `usage`) and exit 1 with `grain: a command is required`.

**Pinned by:** `pkg/cli/daemon_help_test.go`: `TestDaemonHelpUnchanged`; `pkg/cli/upgrade_postgres_test.go`: `TestUpgradePostgresCLIHelp`; `pkg/cli/cli_flags_test.go`: `TestRunCLI_UsageIncludesNewFlags`.

### 6.3 Sandbox flags and help {#CLI-13}

1. In-sandbox flags **MUST** be accepted anywhere among positionals, as `--name value`, `--name=value`, `-n value`, or a bare boolean. `--` ends flags.
2. Every flag a command does not list in `sandboxCommandFlags` **MUST** be refused before the proxy is asked: `grain <cmd>: unknown flag <flag> (flags: <sorted list>|it takes none); run 'grain <cmd> --help' for usage`, status 2.
3. `grain help`, `-h` and `--help` print the full help with the proxy address and the token redacted. `grain help <cmd> [sub]` and `grain <cmd> [sub] --help` print that command's own help (`sandboxCommandHelp`) to stdout with status 0.

**Pinned by:** `pkg/sandboxcli/flags_test.go`: `TestEverySandboxCommandNamesItsFlags`, `TestSandboxCommandsRefuseFlagsTheyDoNotRead`; `pkg/sandboxcli/flags_test.go`: `TestEverySubcommandIsInItsCommandsHelp`, `TestSandboxHelpLeavesOutTheProxyToken`.

---

## 7. The coverage guard {#CLI-14}

The command and flag tables in `host.spec.md` and `sandbox.spec.md` are **generated from the code**, between `<!-- BEGIN GENERATED FLAGS: grain <path> -->` and `<!-- END GENERATED FLAGS -->`. The prose around them is written by hand.

1. `pkg/cli/clispec_test.go` `TestHostCLISpecCoversEveryCommandAndFlag` **MUST** do three things:
   - read the command words `Main` and `dispatchCLI` switch on out of `pkg/cli/main.go` with `go/ast`, so a new top-level command cannot be missed;
   - add the verbs in `hostSpecSubcommands`;
   - run `grain <path> -h` for each path in a child of the test binary, with a clean environment, and parse the flags out of `flag.PrintDefaults` output.

   It **MUST** fail when a path or flag is in the code but not in the spec, or the reverse. Global flags are read from the hand-written usage text.
2. `TestHostCLISpecNamesEveryDefinedFlag` **MUST** fail when any flag `pkg/cli` defines with a literal name (`fs.String("name", …)` and friends) is not named as `` `-name` `` anywhere in `host.spec.md`. This is the backstop for the hand-written-usage commands of [#CLI-12](#CLI-12).
3. `pkg/sandboxcli/clispec_test.go` `TestSandboxCLISpecCoversEveryCommandAndFlag` **MUST** fail when `sandboxCommandFlags` (the list the sandbox CLI enforces) and the spec's sandbox tables disagree.
4. `GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` **MUST** rewrite the generated tables in place. A command the spec lacks is appended under "Unplaced commands", to be moved next to its prose.

The guard does **not** check:
- flags of in-tree capability servers (`grain capability <server> …`), which belong to each server's package;
- flags `pkg/cli` builds with non-literal names;
- subcommand verbs missing from the hand-kept `hostSpecSubcommands` list: they are never run with `-h`, so a new verb is not noticed, and a flag added to one is caught only by the literal-name test. Today that is `access list|allow|deny` and `db upgrade-postgres|upgrade-sqlite-to-postgres` (none of which has flags of its own);
- the per-tool flags of `grain cap`, which are generated from each tool's schema at run time.

**Pinned by:** the three tests named above.

---

## 8. Divergences

Places where a design says something the code does not do. The designs were not changed here.

1. **`external-cli.design.md` and `external-cli.spec.md`** (task #2767):
   - The binary registers **no** `grain skill` or `grain mcp` command. Contexts, `grain login`, `logout`, `whoami`, `cli.RegisterCredentialSource` and the sandbox CLI's yielding for `-server`, `-context` and `$GRAIN_CONTEXT` (XC-1–XC-4, XC-7–XC-10) are implemented ([#CLI-4](#CLI-4), [`host.spec.md` #CLI-108](host.spec.md#CLI-108)).
2. **`sandbox-first-agent-surface.design.md`:**
   - §4 says every subcommand has a `--json` mode "where applicable". In the code only `notify`, `task list`, `task wait`, `task propose`, `api` and `cap` take `--json`; every other command prints the proxy's text.
   - §7 writes `grain log [--since ...] [--grep ...]`. The code's substring filter is `--contains`, and `--grep` is refused as an unknown flag.
   - §4's prompt bullet writes `grain metrics [--section <name>]`. The code takes the section as a positional argument (`grain metrics <section>`), and `--section` is refused.
   - The design leaves write commands to MCP tools, but the CLI also has `status`, `handover write`, `task message`, `task propose`, `notify`, `pr reply|resolve|review-comment`, `git revert`, `memory write|doc-write`, `quarantine`, `release-notes publish`, `api` and `cap`.
   - The turn-ending controls stay MCP-only. `task wait --park`/`--resume-at` are refused, and `task propose` refuses `--supersedes`, `--auto-merge` and `--hold-for-approval=false` ([`sandbox.spec.md` #CLI-93](sandbox.spec.md#CLI-93)).
3. **`cli-run-location.design.md`** is about where agent CLIs (claude, codex, agy) run, not about `grain`'s own CLI. Its command-line surface is the daemon's `-claude-run`, `-codex-run` and `-agy-run` flags and `grain hostd`, and both exist as it describes ([`host.spec.md`](host.spec.md), `grain daemon` and `grain hostd`). No divergence was found in the CLI surface.
4. **Commands the task brief names that do not exist:**
   - There is no `grain upgrade` command: schema upgrades are `grain state prepare-upgrade`, and the store move is `grain upgrade-postgres`/`grain db upgrade-postgres`.
   - There is no `grain executor` command.
   - `grain create` and `grain ci` are task-CLI commands, not modes.

**Pinned by:** nothing. These are findings to fix in the code or the designs.
