# A host agent contract that rarely changes

> **Proposal, with its guard rails built.** This document defines host
> agent contract v1 -- what a grain daemon and an installed `grain-agentd`
> (or `grain host -server`) may say to each other -- and the rules that
> keep it from moving. The guard rails in *Guard rails* are implemented
> alongside it (`pkg/hostd/features.go`, `pkg/hostd/contract_test.go`,
> `tests/agentdcompat`, `scripts/agentd-compat.sh`,
> `.grain/triggers/agentd-contract.yml`). The code moves it calls for are
> follow-up tasks, listed at the end. Nothing in core here is specific to
> one company's infrastructure.

## The problem

`grain-agentd` and `grain host -server` are installed on operators' *other*
machines -- workstations, build boxes, a laptop -- and dial in to the
controller (`pkg/hostd/dialout.go`, `docs/grain-agentd.md`). Updating them
is manual: someone re-runs the install one-liner from **Settings → Hosts**
on every machine. There is no self-update. On the operator's own deployment
an enrolled host is still on `v3.2.0-dev.36` while the daemon is on
`v3.5.0-dev`.

So every change that needs a new host agent costs a walk round every
machine, and until that walk happens the host either refuses the work
("grain hostd is updating or mismatched"), or silently does a worse job of
it. The operator wants the host agent's API to be **unlikely to change**.

The protocol is already versioned (`pkg/hostd/protocol.go`: a Hello
exchange, `Compatible`/`MismatchError`, `Supports*` gates), so a mismatch
fails loudly. But a version number does not by itself keep things still:

- the protocol has moved four times -- detached runs and attach (2),
  socket owner (3), stage/listen/carry for remote hosts (4) -- and an op
  (`renew`) has since been added *without* a bump;
- much of what a remote host does depends on the agentd **build**, not the
  protocol: the allowlist schema, the agent hooks it answers, how it finds a
  CLI's login, its reconnect and renewal timing, its stage-confinement
  rules.

The fix is the usual one for a protocol whose far ends are not upgraded
together: make the far end a small set of generic *mechanisms*, frozen;
move every *policy* and every piece of CLI knowledge to the side that is
upgraded easily -- the daemon -- and send it per request; and grow the
contract only by optional, feature-flagged additions that the daemon checks
before use and degrades without.

## Inventory: what agentd and the daemon share

`grain-agentd` is `hostd.Server` with `AgentsOnly: true`, a remote dialer
and the two hook subcommands (`pkg/agentd/agentd.go`). Everything below
crosses the boundary between it and the daemon, or between it and the next
build of itself.

### Wire: the session and the frames

| Layer | What | Where |
|---|---|---|
| Dial-out | `GET /api/hostd/connect`, an HTTP/1.1 upgrade dressed as a WebSocket handshake, then a mux session over which the daemon opens one stream per request and the host opens `carry` and `renew` streams back | `dialout.go`, `pkg/tunnel/wsupgrade`, `pkg/tunnel/mux` |
| Dial-out headers | `X-Grain-Hostd-Token`, `-Host`, `-Name`, `-Build`, `-Protocol`; key-bound hosts also sign with `-Timestamp`, `-Nonce`, `-Signature` | `dialout.go`, `enroll.go` |
| Enrollment | `POST /api/hostd/join`, `GET /api/hostd/join/{id}/poll`, Ed25519 key, SAS pairing code | `enroll.go` |
| Framing | one byte type, four-byte big-endian length, payload ≤ 1 MiB; first frame a JSON `Request` | `protocol.go` |
| Ops | `start`, `list`, `hello`, `processes`, `attach`, `cancel`, `drain`, `socket_owner`, `stage`, `listen`, `carry`, `renew` | `protocol.go` |
| Frames | `R I C S` (daemon→host), `P O E X F L H Q K W Y N T` (host→daemon) | `protocol.go` |
| JSON types | `Request`, `Hello`, `Process`, `Cursor`, `SignalMsg`, `Started`, `Exit`, `Refusal`, `Offer`, `SocketOwner`, `StageOp`, `StageReply`, `Listening`, `RenewProof`, `RenewReply` | `protocol.go`, `carry.go`, `allowlist.go`, `enroll.go` |
| Stage ops | `mkdir_temp`, `mkdir_all`, `write_file`, `create_temp`, `symlink`, `read_tail`, `remove_all`, `prune`; ≤ 512 KiB per op | `carry.go`, `stage_linux.go` |
| Signals | `TERM`, `INT`, `HUP`, `KILL` to the process group | `protocol.go` |

The appendix is the complete, generated listing of every field, constant
and key, each with the feature it ships under. `contract_test.go` holds it
equal to the code.

### The host owner's allowlist

`~/.config/grain/agentd.yaml` for agentd, `/etc/grain/hostd.yaml` for a
root-installed host (`allowlist.go`). Parsed **strictly**
(`KnownFields(true)`, ignoring `x-*` annotation keys at top level and on entries): a key this build does not know is a parse error.

| Key | Meaning |
|---|---|
| `clis.<name>` / `capabilities.<name>` | one program the host may start; agentd refuses any `capabilities:` |
| `path` / `command` | the binary, absolute (`path` also accepts `~/`); the request names the entry, never a binary |
| `args`, `extra_args` | prepended argv; whether the daemon may append (default yes for a CLI) |
| `paths_under`, `cwd_under` | where path-like args and the working directory must sit, absolute or `~/` (the stage root is added for a start over a session) |
| `env` | names, `NAME_*` patterns, or `*` (any name outside the frozen deny set); identity, `LD_*` and injection variables only by exact name |
| `pass_env`, `set_env` | host-supplied variables the daemon may not override |
| `max_duration` | cap on one process's life (24 h default) |
| `drain_cap`, `capacity`, `labels` | drain bound, concurrent CLI runs, display labels |
| `logins` | CLI login files on the host (`name -> path`, `~/` allowed), reported by name in `Hello.logins` and allowed as `stage symlink` targets (`Hello.login_targets`; built-in table when absent) |

### Built-in entries (local hostd only)

`hostd.BuiltinEntry` / `intree.HostdBuiltins`: grain's own capability
servers, plus whatever an edition registers (`intree.RegisterHostdEntry`) --
the Google edition's corp capability entries, such as its proxy-check
entry, are the example. These are offered by a root-installed **local**
hostd that runs grain's own binary (`EnsureBuiltins`, `-ensure-builtins`),
and they are build-bound by design: the local hostd is upgraded with the
daemon through `grain-hostd-upgrade.path`. agentd registers **none**
(`AgentsOnly`), and contract v1 keeps it that way.

### On-disk layout

| Path | Owner | Contract between |
|---|---|---|
| `<state-dir>/spool/<id>/{process.json,stdout,stderr,exit.json}` | agentd | agentd and **its next build** (a cutover or self-update must adopt the spools it finds: `loadSpools`); the daemon reads them only through `attach` |
| `~/.grain/hostd-stage` (`Hello.StageRoot`) | daemon, via `stage` | the daemon lays out `sessions/` and run directories; agentd only confines (`tmp/` for `mkdir_temp`) |
| `~/.config/grain/agentd-{token,key}`, `agentd.yaml`, `~/.config/systemd/user/grain-agentd.service`, `~/.local/bin/grain-agentd` | installer | installer and agentd |

### Environment and process contract

- Identity variables every child gets from agentd's own environment:
  `PATH HOME USER LOGNAME LANG TZ`.
- agentd's own inputs: `GRAIN_HOSTD_TOKEN`, `GRAIN_HOST_JOIN_CODE`,
  `XDG_RUNTIME_DIR` (socket), `HOME`.
- CLI knowledge read from the environment: `CODEX_HOME`,
  `CLAUDE_CONFIG_DIR` (login discovery, `logins.go`, `stage_linux.go`).
- Hooks a CLI forks: `<Hello.Grain> agy-tool-hook` and
  `<Hello.Grain> claude-stop-hook` -- for a remote run, `Hello.Grain` is the
  agentd binary itself, so **agentd answers the hooks**.
- Mechanism defaults compiled in: `WaitDelay` 5 s, `DefaultMaxDuration`
  24 h, `DefaultExitedRetention` 24 h, `DefaultRenewInterval` 12 h, a 5 s
  cap on reconnect backoff.

## How often it has changed

`git log` on `pkg/hostd`, `pkg/runhost`, `pkg/agentd` and
`cmd/grain-agentd` since this repository's history begins (27 October;
protocols 1-3 predate it) has 59 commits. Most are daemon-side (`remote.go`,
`client.go`, `pkg/runhost`) and needed nothing on the host. These are the
ones that **needed a new host agent** to take effect:

| Date | Commit | Change | Kind |
|---|---|---|---|
| 10-27 | `0c5599b16` | hosts dial out to the controller; four new headers | new surface |
| 10-29 | `b0ce94954` | cap reconnect backoff at 5 s | host behaviour (timing) |
| 10-30 | `c59df9482` | offer missing built-in entries | build-bound (local hostd) |
| 11-01 | `ae9ead7ef` | re-read `drain_cap` on every drain pass | host behaviour |
| 11-01 | `8192bf8da` | bind the socket without touching the umask | host behaviour |
| 11-02 | `70db1cd58`, `a1eae5909`, `8c3bfd9ae` | `stage`, `listen`, `carry` (protocol 4) | new ops |
| 11-02 | `01bfca7bf` | harden stage confinement and remote CLI paths | host security fix |
| 11-02 | `a3dbe8057` | edition-registered built-in entries | build-bound (local hostd) |
| 11-02 | `e706232d7` | registry tokens; `X-Grain-Hostd-Name` | new header (optional) |
| 11-02 | `09afdd2e8` | `capacity:`/`labels:` in the allowlist and Hello | new allowlist keys -- **an older agentd refuses an allowlist that uses them** |
| 11-05 | `e872a2389` | grain-agentd extracted | new binary |
| 11-05..11-06 | `33bfeba26`, `d089f994f` | grain's MCP tool roster in the agy hook changed | CLI policy compiled into agentd |
| 11-06 | `c93a4c486`, `06d7b222a`, `91da4bcab`, `bb42ab185` | Ed25519 key-bound transport, `-join`, `renew` op and frame (no protocol bump) | new op, auth |
| 11-07 | `7b3f69ac6` | `Hello.Logins` | new field; "absent" is ambiguous |
| 11-08 | `cc83be491` | keep the renewal schedule across reconnects | host fix -- **an older host that reconnects often lets its 30-day credential expire** |
| 11-08 | `b744ed317` | `Hello.AgentsOnly` | new field; "absent" is ambiguous |
| 11-10 | `b8d00b09d` | the Claude stop rule learned `resume_later` | CLI policy compiled into agentd |

That is **about eighteen host-agent-affecting changes in two weeks**: five
wire additions, three changes of CLI policy that agentd answers on the
daemon's behalf, six host behaviour or security fixes, and two credential
or enrollment changes. Only the wire additions were visible to the version
check; the policy changes silently made remote runs behave differently
from local ones (an agy run on an old agentd denies a tool grain has since
published), and the renewal fix is the kind of bug that only a host update
cures.

## Contract v1

Contract v1 is **protocol 4 plus a feature list**, frozen. It is the
surface every accepted host agent already has; nothing in it is new
behaviour, only new discipline.

### Primitives

Mechanism lives in agentd. Each is generic -- none knows what an agent CLI
is:

1. **Hello** -- each side says its protocol, minimum, build and its
   **features** (`Hello.Features`). Host facts (`stage_root`, `grain`,
   `capacity`, `labels`, `logins`, `agents_only`) ride along.
2. **Start a process** -- an allowlist entry by name, with argv, env,
   directory and deadline from the daemon, checked against the entry;
   attached (stdio over the stream) or **detached** (spooled on the host,
   outliving the connection until its deadline).
3. **Stdin, signal** -- `I`/`C` frames and `TERM`/`INT`/`HUP`/`KILL` to
   the group.
4. **List, attach, cancel** -- `processes`, `attach` from a byte cursor,
   `cancel`.
5. **Stage files** -- eight filesystem operations confined to the stage
   root.
6. **Listen/forward** -- a loopback listener on the host whose connections
   are carried back to the daemon.
7. **Drain** -- graceful drain, or an idle cutover.
8. **Logins discovery** -- which CLI logins exist on the host, as names.
9. **Renew** -- in-band credential renewal by the host's key.

### Policy and CLI knowledge live in the daemon

Everything that depends on *which* CLI, *which* model or *which* grain
release is the daemon's, and is sent per request within the bounds the host
owner's allowlist sets:

- agent CLI flags, environment, config files (staged), MCP endpoints
  (carried), model and CLI quirks;
- retry, reattach and timeout policy (the daemon already owns reattach:
  `runhost.Hostd.ReattachTimeout`); per-request deadlines;
- what a CLI's hooks decide;
- which files are a CLI's login.

Nothing CLI-specific is compiled into agentd. Today that is violated in
these places, each a follow-up below:

| # | Violation | Where | Move to |
|---|---|---|---|
| V1 | the agy pre-tool hook's **tool roster** (`PublishedTools`) and host-tool grants are compiled in; agentd answers `agy-tool-hook` | `pkg/agent/antigravity/hook`, `agentd.Main` | a per-run **hook policy file** the daemon stages beside the run's config; agentd's hook becomes a generic matcher over it (feature `hook_policy`) |
| V2 | the Claude **stop rule** is compiled in; agentd answers `claude-stop-hook` | `pkg/agent/claude/stophook`, `pkg/agent/stoprule` | the same policy file carries the rule's parameters (closing tools, phrases); agentd evaluates data, not code |
| V3 | `Hello.CodexAuth`, and `stage symlink` allowed to name only codex's login | `stage_linux.go` | a host-owned `logins:` map in the allowlist; `symlink` may target any path it names (feature `logins_map`) |
| V4 | login detection paths for codex, claude and antigravity compiled in | `logins.go` | the same `logins:` map; the built-in table remains only as the default for an allowlist without one |
| V5 | `install` knows where agy lives and which env names it needs | `pkg/agentd/install.go` | the controller's install endpoint serves the allowlist template; the binary carries none |
| V6 | reconnect backoff cap, renewal interval, `WaitDelay`, exited-spool retention compiled in | `remote_linux.go`, `server_linux.go`, `protocol.go` | keep as defaults; let the daemon send hints within fixed host-side bounds (low priority: none has needed changing per CLI) |

V1 and V2 are the most expensive: every new grain MCP tool, and every
stop-rule change, is today a behaviour difference between local and remote
runs until each host is updated. Policy-as-data also suits detached runs,
whose hooks fire while the daemon may be restarting -- a hook that called
back to the daemon would fail exactly when it is needed, so the policy must
be on the host, staged per run.

### Features

A feature is a named, documented promise. Each side lists the features its
build implements in `Hello.Features`; the daemon checks `Hello.Has(f)`
before relying on one, and does without it when the host lacks it. A peer
that predates the list is credited from its protocol version
(`legacyFeatures`), so existing hosts lose nothing.

| Feature | Since | Promises | A daemon facing a host without it |
|---|---|---|---|
| `start` | protocol 1 | `start` attached, `list`, stdin, signals, exit; `hello` | cannot use the host |
| `detach` | protocol 2 | detached starts, `processes`, `attach` from a cursor, `cancel`; `Started.id` | streams attached only; a daemon restart ends the run |
| `drain` | protocol 2 | `drain` and idle cutover; `Hello.draining`/`cutover`; draining refusals | cannot drain the host for an upgrade |
| `socket_owner` | protocol 3 | `socket_owner` | cannot ask the host who holds a socket; its own `/proc` lookup is all it has |
| `stage` | protocol 4 | `stage` confined to `Hello.stage_root`; `Hello.remote`/`grain`/`stage_root` | cannot run a remote CLI (needs files staged) |
| `listen` | protocol 4 | `listen` and the `carry` streams back | cannot carry grain's endpoints to the host |
| `renew` | contract v1 | in-band credential renewal | the host re-enrolls when its credential expires |
| `logins` | contract v1 | `Hello.logins` is authoritative: empty means none | treats logins as unknown |
| `agents_only` | contract v1 | `Hello.agents_only` is authoritative | treats it as unknown |
| `features` | contract v1 | `Hello.features` itself, so silence about a feature is a statement | infers features from the protocol version |
| `env_open` | contract v1 | `env: ["*"]` (any variable outside the frozen deny set), `~/`-relative `path`/`paths_under`/`cwd_under`, and ignored `x-*` annotation keys | installer and Settings snippets write closed `env:` lists and absolute paths |
| `logins_map` | contract v1+ | top-level `logins:` map in the allowlist; `Hello.login_targets` and `stage symlink` accept any regular file it names (`Hello.codex_auth` deprecated) | falls back to `Hello.codex_auth` for codex's login |
| `hook_policy` | contract v1+ | `agy-tool-hook` and `claude-stop-hook` accept `-policy` and evaluate a staged per-run hook policy file (`pkg/agent/hookpolicy`) | stages no policy file and omits `-policy`; hooks fall back to the roster and stop rule compiled into the host binary |
| `handover` | contract v1+ | `Request.handover` on `drain` (`idle`) hands live detached CLI runs over across an in-place re-exec and adopts them in `loadSpools`; `Exit.draining` marks processes ended by hostd's own drain or stop | cuts over only when idle, and cannot distinguish drain-cap kills on `Exit` |
| `env_deny_v2` | contract v1+ | `env: ["*"]` and `NAME_*` patterns also refuse the `env_deny_v2` names (`DYLD_*`, `GIT_*`, `JAVA_TOOL_OPTIONS`, `NODE_PATH`, ...); only an exact name admits one | nothing to do: the daemon sends none of those names, and an exact name is admitted by every host |

`renew`, `logins` and `agents_only` shipped before the feature list existed;
hosts on those builds are credited with neither, which is exactly the
"not known" reading the daemon already gives them.

## Extensibility rules

These are the rules every change to the contract follows. The first four
are what keeps a host installed today working with the daemons of the next
years.

1. **Every JSON field is optional, and unknown fields are ignored, in both
   directions.** Decoders never use `DisallowUnknownFields`; encoders use
   `omitempty`/`omitzero`. The zero value of a new field must mean "the old
   behaviour".
2. **New behaviour is a new feature.** A new op, frame, stage operation,
   field whose absence would be misread, or field whose *being ignored*
   would be unsafe (a new restriction the daemon asks for) ships under a new
   feature name. The sender checks `Has` first; the daemon **degrades
   gracefully** without it -- holds the work for another host, falls back,
   or says in the run why the host cannot do it -- and never fails a run
   because a host is older. A field that is merely informational, whose
   absence an old peer reads correctly, may ride on an existing feature.
3. **Never change a meaning.** A field, op, frame byte, stage op or feature
   name, once released, keeps its meaning forever. If it must mean something
   else, add a new one.
4. **ProtocolVersion is frozen at 4.** It moves only for a major change
   (below). An unknown op is refused (`unknown op`) and the daemon treats
   that refusal as "feature absent", which is what the feature check
   prevents in the first place.
5. **Mechanism defaults never move silently.** A default compiled into
   agentd (timeouts, retention, confinement) may be tightened only as a
   security fix; otherwise the daemon sends what it wants per request.
6. **The on-host spool format is frozen.** A newer agentd must adopt an
   older agentd's spools; anything new in `process.json` or `exit.json` is
   an optional field.
7. **The allowlist follows the same rules** (next section): new keys are
   optional, defaulting to the old behaviour, and installers and Settings
   never write a key the target host does not advertise.

### Major changes and deprecation

A major contract change -- `ProtocolVersion` 5, or removing a feature -- is
allowed only when all of these hold:

- a security flaw cannot be fixed compatibly, or keeping the old surface
  costs more than the walk round every machine;
- the replacement has shipped as a feature, and the daemon has preferred it,
  for the whole **deprecation window**: **six minor releases and at least
  ninety days**, whichever is longer;
- Settings → Hosts has shown every host still lacking it, by name, for that
  window.

During the window `MinCompatible` stays at or below the old version, the
daemon keeps driving the old path, and the compatibility test keeps the
oldest accepted release in its matrix. After it, the test's oldest release
moves forward and `GRAIN_AGENTD_CONTRACT_REMOVE=1` lets the generated block
drop the line.

## Allowlist stability

The allowlist is the other thing that forces someone to touch a remote
machine: a new CLI version that needs one more environment variable is
refused until the file says so, and a new allowlist key makes an older
agentd refuse the whole file. The schema below keeps the host owner's
control -- **which binaries, which directories, how long, which host
credentials** -- and stops encoding facts about CLI versions.

```yaml
# ~/.config/grain/agentd.yaml -- contract v1 schema, with the proposed
# additions marked.
clis:
  agy:
    path: ~/.local/bin/agy          # proposed: home-relative paths
    paths_under: [~/.grain/hostd-stage]
    cwd_under: [~/.grain/hostd-stage]
    env: ["*"]                      # proposed: any name the daemon sends,
                                    # minus the frozen deny set below
    pass_env: [HTTPS_PROXY, NO_PROXY]
    max_duration: 12h
logins:                             # V3, V4 (feature logins_map)
  antigravity: ~/.gemini/oauth_creds.json
  codex: ~/.codex/auth.json
x-notes: anything                   # proposed: x-* keys are ignored
```

- **Generic entry kinds stay two.** `clis:` entries take a daemon-built
  argv and environment within bounds; `capabilities:` entries run a fixed
  argv. No entry kind names a CLI; the entry's *name* is only a label the
  daemon routes by.
- **`env: ["*"]`** admits any variable the daemon sends except a deny set
  frozen in the contract: the identity variables, `LD_*`, and the
  interpreter and shell injection variables (`BASH_ENV`, `ENV`,
  `NODE_OPTIONS`, `PYTHONSTARTUP`, `PYTHONPATH`, `PERL5OPT`, `RUBYOPT`),
  which feature `env_deny_v2` widens (below). A
  host owner who wants a closed list keeps writing names. A path-like value
  must still sit under `paths_under`; `set_env` and `pass_env` still cannot
  be overridden. For an agents-only host this gives the daemon nothing it
  lacks today -- an agent CLI with a shell tool already runs whatever the
  prompt asks, as the user -- while a new CLI version's new variables stop
  needing a file edit. The installer writes `env: ["*"]` only for hosts
  that advertise the feature (`env_open`).
- **The deny set grows only under a new feature.** The 2026-11-11
  walkthrough (`docs/test-runs/2026-11-11-host-agent-contract.md`, item 2)
  found `"*"` still admitting variables that change what the child loads
  or runs: `JAVA_TOOL_OPTIONS=-javaagent:...` and `GIT_CONFIG_PARAMETERS`
  (not path-like, so `paths_under` never applies), `PROMPT_COMMAND`,
  `SHELLOPTS`, `PS4`, and module paths (`GCONV_PATH`, `NODE_PATH`,
  `PYTHONHOME`, `PERL5LIB`, `RUBYLIB`) under the stage directory the
  daemon writes. The agents-only argument above covers `clis:` entries, but
  `"*"` is accepted on `capabilities:` entries too, whose fixed argv is the
  whole of what the daemon may run there; on one of those, these variables
  are code execution the daemon otherwise lacks. So feature `env_deny_v2`
  adds them (`envDenyV2`: `DYLD_*`, `GCONV_PATH`, `BASHOPTS`, `SHELLOPTS`,
  `PROMPT_COMMAND`, `PS4`, `ZDOTDIR`, `NODE_PATH`, `PYTHONHOME`,
  `PYTHONUSERBASE`, `PERL5LIB`, `PERLLIB`, `PERL5DB`, `RUBYLIB`,
  `PYTHONWARNINGS`, `CLASSPATH`, `JAVA_TOOL_OPTIONS`, `JDK_JAVA_OPTIONS`,
  `_JAVA_OPTIONS`, `GIT_*`, `XDG_CONFIG_HOME`, `OPENSSL_CONF`,
  `OPENSSL_ENGINES`, `OPENSSL_MODULES`, `LUA_INIT*`, `LUA_PATH*`,
  `LUA_CPATH*`) on top of the frozen v1 set, which stays as it is. `PYTHONWARNINGS` is there because the
  interpreter imports a filter's dotted category at startup (with
  `BROWSER`, a stdlib-only way to run a command), and `XDG_CONFIG_HOME`
  because its `git/config` is git's global config, the same reach as
  `GIT_CONFIG_*`. `OPENSSL_CONF` is read at library init by any child
  linking OpenSSL (curl, git over https, python's `ssl`), and a config can
  load an engine or provider shared object -- `GCONV_PATH`'s reach -- and
  Lua runs `LUA_INIT` at startup. (Bash's exported functions, `BASH_FUNC_name%%`, need no
  entry: no valid env name contains `%`.) Growing the set is the one
  change to `"*"` that needs no daemon change: the daemon sends none of
  these names to codex, claude or agy (their `HostdEnv`, held by
  `TestHostdEnvSendsNoDeniedNameButAgyHome`), and an exact name in `env`
  admits any of them on every host. The cost falls on a host owner who
  hand-wrote `"*"` for a capability that needs one of them -- grain's own
  `git` server, placed on a host, reads `GIT_REPO_TOKEN` -- after the
  upgrade the start is refused naming the variable, and listing it by exact
  name fixes it. Generic command variables (`EDITOR`, `PAGER`,
  `SSH_ASKPASS`) and package-manager configuration (`npm_config_*`,
  `GEM_PATH`) were considered and left open: what reads them is a tool the
  child chooses to run, not the child's own startup, and a deny set that
  chased every tool's configuration would never be complete.
- **`logins:`** moves login knowledge from the binary to host-owned data.
  It names the files that are a CLI's login; agentd reports which exist (by
  name) and lets `stage symlink` target only those. A new CLI's login is
  one line the owner adds deliberately -- lending a credential is the owner's
  decision, not the controller's. Without the key the built-in table
  applies, so existing files keep working.
- **Unknown keys.** Strict parsing stays: a typo in a security-relevant
  key (`max_duration`, `extra_args`) must not be silently ignored. Keys
  under `x-` are ignored, for annotations. New keys follow rule 7: optional,
  defaulting to the old behaviour, and never written for a host that does
  not advertise them.
- **Home-relative paths** (`~/`) let one template serve every machine, and
  a stable launcher path (`~/.local/bin/agy`, which a CLI's own updater
  re-points) means a CLI update needs no edit.

New CLIs still need an entry -- that is the owner's control over what runs
-- but adding one is data (`grain-agentd allow <name>` with the controller's
template, confirmed on the host), never a new binary.

## Guard rails

Implemented with this document.

1. **Feature list** (`pkg/hostd/features.go`): `Hello.Features`,
   `Hello.Has`, and `legacyFeatures` crediting older peers from their
   protocol. `SupportsAttach`, `SupportsSocketOwner` and `SupportsCarry`
   now route through it.
2. **Golden contract test** (`pkg/hostd/contract_test.go`): renders every
   wire field, allowlist key, op, frame, stage op, header, login name, the
   identity variables and size bounds into the appendix below, and fails
   unless the appendix matches. Every field and constant must be mapped to a
   feature this build lists (`contractFields`, `contractConsts`), and every
   feature must have a hand-written row in the feature table above. The
   regenerating run (`GRAIN_UPDATE_AGENTD_CONTRACT=1 go test ./pkg/hostd
   -run AgentdContract`) refuses to drop a line unless
   `GRAIN_AGENTD_CONTRACT_REMOVE=1` says the deprecation window is over.
3. **Compatibility test** (`tests/agentdcompat`,
   `scripts/agentd-compat.sh`, the `run host` job of the Tests workflow):
   builds `grain-agentd` from the oldest accepted release (`v3.2.0`, the
   first to ship it) and each later final release, up to the newest five,
   plus this tree's, then drives each with this tree's daemon side
   (`hostd.Remote`, `hostd.Client`): hello and feature check, list,
   attached start with stdin, stdout, stderr and exit code, an allowlist
   refusal, stage (and confinement), listen/carry, a detached start that
   **reattaches after a daemon restart** with nothing lost, cancel, and
   drain. Any regression fails CI. `AGENTD_COMPAT_TAGS` overrides the
   matrix.
4. **Review trigger** (`.grain/triggers/agentd-contract.yml`): a pull
   request touching `protocol.go`, `features.go`, the allowlist schema,
   stage, spool, dial-out, enrollment, logins, the session mux, this
   document or the compatibility test is a contract change, gated for a
   person and `.grain/templates/agentd-contract-review.yml` (alongside
   grain's security review on the classified paths).

## The safety valve: agentd self-update

No freeze is perfect: `01bfca7bf` and `cc83be491` were host-side fixes that
only a new binary cures, and a future one will be too. The valve is an
opt-in self-update that reuses the local host's cutover.

### Design

1. **Advertise.** agentd lists feature `self_update` and says its platform
   (`Hello.platform`, `linux/amd64`) and its update policy from the
   allowlist:
   ```yaml
   update:
     mode: off | offer | auto     # default: offer
     pin: v3.*                    # optional: never leave this range
   ```
2. **Offer.** When a newer release is available for that platform, the
   daemon shows it on Settings → Hosts. In `offer` mode nothing more happens
   until the owner runs `grain-agentd update` on the host (or clicks
   Update, if they also set `mode: auto` for that host). In `auto` mode the
   daemon sends an `update` request.
3. **Fetch.** The binary comes over the existing session (chunked like
   `stage`, since a host may not reach the internet) or from the
   controller's install endpoint, with a **signed manifest**: version,
   platform, SHA-256, minimum contract features, and a signature by the
   **release signing key**.
4. **Verify.** agentd checks the signature against a public key **pinned at
   install time** (in the binary and `~/.config/grain/agentd-release.pub`),
   never one the daemon supplies; checks the hash; refuses a version older
   than its own (anti-rollback) unless the owner's `pin` asks for it; and
   refuses one outside `pin`.
5. **Stage.** Write `~/.local/lib/grain-agentd/versions/<v>/grain-agentd`
   (0755, owned by the user), run it with `-build` and `-check` against the
   current allowlist, and only then move the `current` link
   `~/.local/bin/grain-agentd` points through. The previous version stays
   for rollback.
6. **Cut over.** Exactly local hostd's `grain-hostd-upgrade.path` sequence:
   `Server.Cutover` keeps taking starts and exits at the first moment
   nothing runs (detached runs are adopted from their spools by the new
   build, rule 6); after `drain_cap` it becomes a drain. systemd `--user`
   (`Restart=always`) starts the new binary.
7. **Roll back.** If the new build has not completed a hello with the
   controller within five minutes, a `ExecStartPre` guard moves `current`
   back and the host reports the failed update.

### Security

A controller that can push code to a host is a controller whose compromise
is a compromise of every host. Three things bound that here:

- **The signer is not the controller.** Code is accepted only if signed by
  the release key pinned at install. A compromised controller can at most
  choose *which signed release* a host runs, and anti-rollback plus `pin`
  stop it choosing an old, vulnerable one.
- **Scope.** Only the rootless, agents-only `grain-agentd` self-updates.
  It runs as the user, and the controller can already run an agent CLI with
  a shell tool there; an update adds persistence and escape from the
  allowlist's bounds, not a new privilege. A root-installed `grain host`
  (which runs capability servers) **never** self-updates from the
  controller; it updates with its machine's package or installer.
- **The owner decides.** `mode: off` disables it; `offer` (the default)
  needs a person on the host; `auto` is an explicit opt-in per host. The
  policy lives in the host-owned allowlist, which the controller cannot
  write.

### Build it now, or later?

**Build the owner-run half now; build the push only if the freeze fails.**
`grain-agentd update` (steps 3-7 run by the owner, `offer` mode) is cheap,
replaces re-running the install one-liner, preserves running work through
cutover, and is how a host-side security fix gets out. It needs signed,
per-platform release binaries first. Daemon-initiated `auto` updates should
wait until the freeze has demonstrably failed: **more than one release in
any ninety days that needs a host update for a feature operators use**,
once V1-V5 have moved. Until then, the compatibility test and the feature
list should make that rare.

## Follow-up work

Filed as separate tasks, each after this one lands:

1. **V1 + V2: hook policy as staged data (done)** -- the daemon stages a
   per-run policy file (`pkg/agent/hookpolicy`: published tools/patterns,
   capability-server prefixes, host-tool grants, stop-rule parameters);
   agentd's `agy-tool-hook` and `claude-stop-hook` evaluate it; feature
   `hook_policy`; the daemon keeps today's behaviour for hosts without it.
2. **V3 + V4: host-owned `logins:`** *(done)* -- generic login discovery and
   `stage symlink` targets from the allowlist; feature `logins_map`;
   deprecate `Hello.codex_auth`; the daemon reads `Has(FeatureLogins)` to
   tell "none" from "unknown".
3. **Allowlist `env: ["*"]` and `x-` keys** -- the frozen deny set; feature
   `env_open`; installer and Settings write it only for hosts advertising
   it.
4. **V5: installer template from the controller** -- drop agy discovery and
   the env list from the agentd binary.
5. **Signed per-platform agentd release binaries** -- prerequisite for 6.
6. **`grain-agentd update`** -- the owner-run self-update above, feature
   `self_update`.

## Appendix: the generated contract

Every field, key and constant of contract v1, each with the feature it
ships under. Generated by `pkg/hostd/contract_test.go`; edit the code, then
regenerate.

<!-- agentd-contract:begin (generated by contract_test.go; do not edit by hand) -->
```text
protocol_version 4
min_compatible 1
feature start
feature detach
feature drain
feature socket_owner
feature stage
feature listen
feature renew
feature logins
feature agents_only
feature features
feature env_open
feature logins_map
feature hook_policy
feature handover
feature env_deny_v2
json Request.op hostd.Op [start]
json Request.kind hostd.Kind [start]
json Request.name string [start]
json Request.args []string [start]
json Request.env []string [start]
json Request.dir string [start]
json Request.deadline time.Time [start]
json Request.hello *hostd.Hello [start]
json Request.detach bool [detach]
json Request.run string [detach]
json Request.task string [detach]
json Request.id string [detach]
json Request.cursor hostd.Cursor [detach]
json Request.idle bool [drain]
json Request.handover bool [handover]
json Request.inode uint64 [socket_owner]
json Request.stage *hostd.StageOp [stage]
json Request.renew *hostd.RenewProof [renew]
json Hello.protocol_version int [start]
json Hello.min_compatible int [start]
json Hello.build string [start]
json Hello.draining bool [drain]
json Hello.cutover bool [drain]
json Hello.running string [drain]
json Hello.current string [drain]
json Hello.current_build string [drain]
json Hello.capacity int [start]
json Hello.labels map[string]string [start]
json Hello.remote bool [stage]
json Hello.grain string [stage]
json Hello.stage_root string [stage]
json Hello.codex_auth string [stage]
json Hello.logins []string [logins]
json Hello.login_targets map[string]string [logins_map]
json Hello.agents_only bool [agents_only]
json Hello.features []hostd.Feature [features]
json Process.id string [detach]
json Process.run string [detach]
json Process.task string [detach]
json Process.kind hostd.Kind [detach]
json Process.name string [detach]
json Process.pid int [detach]
json Process.started time.Time [detach]
json Process.deadline time.Time [detach]
json Process.detached bool [detach]
json Process.spool string [detach]
json Process.cursor hostd.Cursor [detach]
json Process.exited bool [detach]
json Process.exit *hostd.Exit [detach]
json Process.attached bool [detach]
json Cursor.stdout int64 [detach]
json Cursor.stderr int64 [detach]
json SignalMsg.signal string [start]
json Started.pid int [start]
json Started.id string [detach]
json Exit.code int [start]
json Exit.signal string [start]
json Exit.deadline bool [start]
json Exit.draining bool [handover]
json Exit.wait_delay bool [start]
json Exit.error string [start]
json Exit.finished_at time.Time [start]
json Refusal.reason string [start]
json Refusal.draining bool [drain]
json Offer.kind hostd.Kind [start]
json Offer.name string [start]
json Offer.path string [start]
json SocketOwner.pid int [socket_owner]
json StageOp.op string [stage]
json StageOp.path string [stage]
json StageOp.dir string [stage]
json StageOp.pattern string [stage]
json StageOp.target string [stage]
json StageOp.data []uint8 [stage]
json StageOp.perm uint32 [stage]
json StageOp.limit int64 [stage]
json StageOp.max_age time.Duration [stage]
json StageReply.path string [stage]
json StageReply.data []uint8 [stage]
json StageReply.truncated bool [stage]
json StageReply.error string [stage]
json StageReply.not_exist bool [stage]
json Listening.addr string [listen]
json RenewProof.current_token string [renew]
json RenewProof.timestamp string [renew]
json RenewProof.nonce string [renew]
json RenewProof.signature string [renew]
json RenewReply.token string [renew]
json RenewReply.expires_at time.Time [renew]
yaml Allowlist.clis map[string]*hostd.Entry [start]
yaml Allowlist.capabilities map[string]*hostd.Entry [start]
yaml Allowlist.drain_cap time.Duration [drain]
yaml Allowlist.capacity int [start]
yaml Allowlist.labels map[string]string [start]
yaml Allowlist.logins map[string]string [logins_map]
yaml Entry.path string [start]
yaml Entry.command string [start]
yaml Entry.args []string [start]
yaml Entry.extra_args *bool [start]
yaml Entry.paths_under []string [start]
yaml Entry.cwd_under []string [start]
yaml Entry.env []string [start]
yaml Entry.pass_env []string [start]
yaml Entry.set_env map[string]string [start]
yaml Entry.max_duration time.Duration [start]
const BuildHeader = "X-Grain-Hostd-Build" [start]
const HostHeader = "X-Grain-Hostd-Host" [start]
const KindCLI = "cli" [start]
const KindCapability = "capability" [start]
const LoginAntigravity = "antigravity" [logins]
const LoginClaude = "claude" [logins]
const LoginCodex = "codex" [logins]
const NameHeader = "X-Grain-Hostd-Name" [start]
const NonceHeader = "X-Grain-Hostd-Nonce" [renew]
const OpAttach = "attach" [detach]
const OpCancel = "cancel" [detach]
const OpCarry = "carry" [listen]
const OpDrain = "drain" [drain]
const OpHello = "hello" [start]
const OpList = "list" [start]
const OpListen = "listen" [listen]
const OpProcesses = "processes" [detach]
const OpRenew = "renew" [renew]
const OpSocketOwner = "socket_owner" [socket_owner]
const OpStage = "stage" [stage]
const OpStart = "start" [start]
const ProbeEnv = "GRAIN_CAPABILITY_PROBE" [start]
const ProtocolHeader = "X-Grain-Hostd-Protocol" [start]
const RemotePath = "/api/hostd/connect" [start]
const SignatureHeader = "X-Grain-Hostd-Signature" [renew]
const StageCreateTemp = "create_temp" [stage]
const StageMkdirAll = "mkdir_all" [stage]
const StageMkdirTemp = "mkdir_temp" [stage]
const StagePrune = "prune" [stage]
const StageReadTail = "read_tail" [stage]
const StageRemoveAll = "remove_all" [stage]
const StageSymlink = "symlink" [stage]
const StageWriteFile = "write_file" [stage]
const TimestampHeader = "X-Grain-Hostd-Timestamp" [renew]
const TokenHeader = "X-Grain-Hostd-Token" [start]
const frameExit = 'X' [start]
const frameHello = 'H' [start]
const frameListening = 'N' [listen]
const frameOK = 'K' [detach]
const frameOffers = 'L' [start]
const frameProcesses = 'Q' [detach]
const frameRefused = 'F' [start]
const frameRenewed = 'T' [renew]
const frameRequest = 'R' [start]
const frameSignal = 'S' [start]
const frameSocketOwner = 'W' [socket_owner]
const frameStaged = 'Y' [stage]
const frameStarted = 'P' [start]
const frameStderr = 'E' [start]
const frameStdin = 'I' [start]
const frameStdinClose = 'C' [start]
const frameStdout = 'O' [start]
identity_env PATH
identity_env HOME
identity_env USER
identity_env LOGNAME
identity_env LANG
identity_env TZ
env_deny PATH
env_deny HOME
env_deny USER
env_deny LOGNAME
env_deny LANG
env_deny TZ
env_deny LD_*
env_deny BASH_ENV
env_deny ENV
env_deny NODE_OPTIONS
env_deny PYTHONSTARTUP
env_deny PYTHONPATH
env_deny PERL5OPT
env_deny RUBYOPT
env_deny_v2 DYLD_*
env_deny_v2 GCONV_PATH
env_deny_v2 BASHOPTS
env_deny_v2 SHELLOPTS
env_deny_v2 PROMPT_COMMAND
env_deny_v2 PS4
env_deny_v2 ZDOTDIR
env_deny_v2 NODE_PATH
env_deny_v2 PYTHONHOME
env_deny_v2 PYTHONUSERBASE
env_deny_v2 PERL5LIB
env_deny_v2 PERLLIB
env_deny_v2 PERL5DB
env_deny_v2 RUBYLIB
env_deny_v2 PYTHONWARNINGS
env_deny_v2 CLASSPATH
env_deny_v2 JAVA_TOOL_OPTIONS
env_deny_v2 JDK_JAVA_OPTIONS
env_deny_v2 _JAVA_OPTIONS
env_deny_v2 GIT_*
env_deny_v2 XDG_CONFIG_HOME
env_deny_v2 OPENSSL_CONF
env_deny_v2 OPENSSL_ENGINES
env_deny_v2 OPENSSL_MODULES
env_deny_v2 LUA_INIT*
env_deny_v2 LUA_PATH*
env_deny_v2 LUA_CPATH*
max_frame 1048576
max_stage_data 524288
```
<!-- agentd-contract:end -->
