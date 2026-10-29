# Writing a Grain Capability Server (1-page guide)

Capabilities are how Grain equips an isolated sandbox VM or CI container with external access and tools for the duration of a single attempt: your server mints what the run needs, Grain materializes it into the sandbox before the agent or CI steps start, and every stateful resource is revoked on `release_<method>` when the attempt ends (or expires at `deadline` if that call never arrives). A capability can provide any mix of:

- **Temporary credentials & files** (`placements`) — a short-lived cloud key, an API token, a `.git-credentials` entry, or a static skill/playbook written into the sandbox filesystem (spec §6.1 `#gcp-key`, §6.4 `#skill`);
- **Attempt-scoped MCP toolsets** (`mcp_endpoint`) — custom MCP tools served at `/runs/<attempt_id>/mcp` and proxied to the agent CLI and CI container (spec §6.2 `#self-debug`);
- **Local services, proxies & tunnels** (`ports`) — an authenticated host-side proxy or tunnel (a Git proxy, a model proxy, a database tunnel) forwarded into sandbox loopback (spec §6.3 `#git-repo`, `#model-proxy`);
- **Working-tree and sandbox setup** (`setup_root`, `setup`) — a command Grain runs in the sandbox before the agent starts: `setup_root`, from the one grant per attempt that initializes the root working directory the agent or CI steps start in (a `#git-repo` with `root: true` cloning the primary repository into it), and `setup`, from any number of grants, for everything after it (a `root: false` `#git-repo` cloning an additional repository beside the root, a credential capability signing its CLI in) (spec §5.5, §6.3);
- **Agent instructions** (`prompt_section`) — a note telling the agent what was provisioned and how to use it.

**Normative specification:** [docs/capabilities-spec.md](capabilities-spec.md) — it wins wherever this page is briefer. **Status:** this architecture ships alongside the built-in in-process registry in `pkg/capability/` ([docs/ci-capabilities.md](ci-capabilities.md)). Grain's side of the protocol — references, grants and argument validation, the `mcpServers` entry, the stdio and HTTP client, mint-response validation and the admission pass — and a Go helper for writing a capability server (`protocol.NewMethod`, `protocol.NewCapabilityServer`) are in [`pkg/capability/protocol`](../pkg/capability/protocol), and the in-tree servers built on it are served by `grain capability <name>` and registered on every deployment as built-ins ([below](#in-tree-capability-servers-grain-capability-name)). A pass is materialized: admission mints every reference grant before the sandbox is built (`pkg/orchestrator/capability_pass.go`), and the pass is applied to task runs and grain CI jobs in §5's order (`capability_materialize.go`): ports are forwarded, placements written, `setup_root` and `setup` run, then each `mcp_endpoint` proxied and each `prompt_section` rendered (`capability_endpoints.go`). `recreate_sandbox` re-applies it without minting, and every stateful grant is released when the attempt ends. What is not wired: a grant returning `ports` is refused on a deployment that serves no capability port relay, a sandbox backend that cannot run commands cannot take a pass at all, and the in-process registry is still what runs bare-name grants such as `gcp-key`.

## What you build

A capability is a standard **MCP server** (written in any language with any MCP SDK) configured in the deployment's `grain/config` state repository (and through the Settings UI / CLI) under `mcpServers` following Antigravity (`agy`)'s `mcp_config.json` specification shape ([docs/agy-surface.md](agy-surface.md) lines 145–174 and 1542–1663; spec §3.1):

1. **Stdio (`command`, `args`, `env`)** — for **in-tree capabilities** built into the `grain` binary (`"command": "grain", "args": ["capability", "<name>"]`) and **in-config (`grain/config`) capability plugins** committed to `grain/config` (where a relative `command` such as `"plugins/custom-plugin/server"` is resolved from the root of the checked-out `grain/config` state repository, matching [docs/custom-agent-backends.md](custom-agent-backends.md)). Grain spawns the executable with an isolated environment and communicates over stdin/stdout.
2. **Streamable HTTP (`serverUrl`, `headers`)** — for **external capability servers** running outside `grain` and `grain/config` (standalone binary, `systemd` unit, container, or remote HTTP endpoint at `POST <serverUrl>`).

Sensitive `env` and `headers` values (root minter keys, API keys, bearer tokens) are stored in **Grain's encrypted secrets store** and referenced via `$secret:<name>` in `grain/config`, never committed in cleartext. Grain calls two control-plane tools per capability method (`mint_<method>` and `release_<method>`) and materializes what they return into the run's sandbox. Your server is **fully trusted inside every sandbox it serves** — its files, prompt text, and tools act with the run's authority — so treat it like production infrastructure.

## The contract

| | What Grain does | What your server does |
| :--- | :--- | :--- |
| **Config & Auth** | Reads `mcpServers` from `grain/config` (editable in Settings UI / CLI; supports `enable`/`disable` via `disabled`) and resolves `$secret:<name>` in `env` and `headers` from Grain's encrypted secrets store at runtime (spec §3.1). Spawns stdio servers with an isolated `env`; sends resolved `headers` (e.g. `Authorization: Bearer <token>`) on every control-plane and proxied data-plane HTTP request. | Stdio servers read root credentials from `env` and speak MCP on stdin/stdout. External HTTP servers return `401` to any request on `/mcp` or `/runs/…` without the configured `Authorization: Bearer <token>` header — loopback is not a trust boundary. |
| **Configuration** (optional) | If you advertise a `configure` tool, discovers its `inputSchema.properties.config` schema (same keyword subset as `args`), validates the entry's `config` against it, resolves its secret fields (`writeOnly: true` or `format: password`, stored only in the secrets store as `$secret:<name>`), and calls `configure({config})` after every `initialize`, before any mint, and again whenever the configuration changes; then re-lists your tools. A failing `configure` marks the server unhealthy and refuses its mints (spec §4.5). | Applies `configure` idempotently and keeps advertising it; returns `{"status": "ok"}` or `isError: true` saying what is wrong. Methods may depend on the configuration (an `enum` of configured accounts). Without `configure`, read settings from `env`/`headers` as before. |
| **Discovery** | Standard MCP client over stdio or Streamable HTTP: `initialize`, then `tools/list` (follows `nextCursor`). Every `mint_<method>` tool is a capability `#<method>`; `release_<method>` is its teardown. No custom metadata. | Registers `mint_<method>` with `inputSchema` `{attempt_id, deadline, args}` (all three required; `properties.args` is your `with:` schema with strict allowlists) and `release_<method>` with `{attempt_id}`. Stateless methods (files/prompt only) omit `release_`. |
| **Mint** | After the sandbox permit is acquired and before the VM boots, calls `mint_<method>({attempt_id, deadline, args})` once per grant — `attempt_id` (`e1010c47-175-2-0`) is unique per grant, `deadline` is an RFC 3339 instant after which the attempt cannot be running, `args` is the validated `with:` object (`{}` if none). 60 s timeout. | Returns `{status, placements?, mcp_endpoint?, ports?, setup_root?, setup?, prompt_section?}` (or `status: "refused"` + `reason`, or `status: "resource_exhausted"` + optional RFC 3339 `retry_after`). Keys everything under `attempt_id`, expires it itself at `deadline` (+ a short grace), and is **idempotent per `attempt_id`**: a repeat call returns the same result; a repeat with a later `deadline` extends the lease (that is how Grain renews long runs). Cannot honour `deadline`? Return `refused`, not a shorter credential. |
| **Materialize** | In this order: forwards `ports` into the sandbox loopback; writes `placements` (clean absolute paths; `0600` default; no setuid; Grain-owned paths such as `~/.grain`, agent CLI config dirs, `/etc/sudoers*` are refused; ≤ 1 MiB each); runs the setup commands (next row); proxies `mcp_endpoint` (HTTP path proxy for external HTTP servers, or `mcp.StartCommandHTTP` / stdio proxy for stdio servers), discovers `serverInfo.name` and tools via `initialize` + `tools/list`, and wires the server into the active agent CLI's native per-run config (`agy`, `claude`, `codex`, custom) and CI `~/.grain/mcp.json` (spec §5.2.4); appends `prompt_section` (≤ 8 KiB) in a block attributed to your capability; then launches the agent CLI or CI steps in the root working directory. | `mcp_endpoint` is **exactly** `/runs/<attempt_id>/mcp` (a separate per-attempt MCP toolset, `serverInfo.name` matching `^[a-z0-9][a-z0-9_-]{0,62}$`, not `grain-sandbox`). `ports` are `{host_port, sandbox_port?}` on a dedicated data-plane listener (not your `/mcp` port) owned by your own service, authenticating each request with a per-attempt token you placed. No secrets in `prompt_section` or `reason`. |
| **Setup** | Once every grant's `ports` are bridged and `placements` are on disk, runs the one `setup_root` any grant returned, then each grant's `setup` in grant order — each executed directly (no shell, no arguments) as `debian`, with `cwd` set to the root working directory, under a bounded timeout (10 min default) — before any MCP endpoint is wired or the agent starts. A command that exits non-zero or times out aborts the attempt and releases every grant; two grants returning a non-empty `setup_root` refuse the run before it boots, naming both. | Return `setup_root` only from the grant that initializes the root working directory (a `#git-repo` with `root: true` cloning the primary repository into it) and `setup` for everything else (a `root: false` `#git-repo` cloning an additional repository to `/home/debian/repos/<owner>/<repo>`; a credential capability activating its key). Each is the clean absolute path of an executable you placed (`mode: "0700"`); bake every argument into that script rather than into a shell line, and keep secrets out of its output. A method that can return `setup_root` must also offer an `args` mode that does not (`root: false`). |
| **Release** | When the attempt ends: `release_<method>({attempt_id})` for every stateful grant (30 s timeout; retried on failure). `recreate_sandbox` re-materializes the cached result — `ports`, `placements`, then `setup_root` and `setup` again in the fresh VM — and does not re-mint. | Revokes the credential, unregisters `/runs/<attempt_id>/mcp`, invalidates the port token — and returns `{"status": "ok"}` even if already released or expired. |

## A complete server (Python, `mcp` FastMCP)

```python
import os, sys, uvicorn
from pydantic import BaseModel, Field
from mcp.server.fastmcp import FastMCP
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.responses import JSONResponse

mcp = FastMCP("my-cap", stateless_http=True)        # stdio or POST /mcp
leases: dict[str, dict] = {}                        # attempt_id -> cached mint result

class Args(BaseModel):                              # becomes inputSchema.properties.args
    model_config = {"extra": "forbid"}
    account: str = Field("staging", pattern=r"^[a-z0-9][a-z0-9_-]{0,62}$")

@mcp.tool(name="mint_my-method")
def mint(attempt_id: str, deadline: str, args: Args) -> dict:
    if args.account not in ("staging", "prod"):
        return {"status": "refused", "reason": f"no account named {args.account!r}"}
    if attempt_id not in leases:                    # idempotent per attempt_id
        key = create_upstream_key(args.account, expires_at=deadline)   # uses os.environ["PLUGIN_API_KEY"]
        leases[attempt_id] = {
            "status": "ok",
            "placements": [{"path": "/home/debian/.my-cred.json", "mode": "0600",
                            "content": key.material}],
            "prompt_section": f"A `{args.account}` credential is at `/home/debian/.my-cred.json`.",
        }
    else:
        extend_upstream_key(attempt_id, expires_at=deadline)          # renewal
    return leases[attempt_id]

@mcp.tool(name="release_my-method")
def release(attempt_id: str) -> dict:
    if leases.pop(attempt_id, None):
        delete_upstream_key(attempt_id)                               # your code
    return {"status": "ok"}

if "--http" in sys.argv:                            # external HTTP server (serverUrl + headers)
    TOKEN = os.environ["EXTERNAL_CAP_TOKEN"]
    class RequireToken(BaseHTTPMiddleware):
        async def dispatch(self, request, call_next):
            if request.headers.get("authorization") != f"Bearer {TOKEN}":
                return JSONResponse({"error": "unauthorized"}, status_code=401)
            return await call_next(request)
    app = mcp.streamable_http_app()
    app.add_middleware(RequireToken)
    uvicorn.run(app, host="127.0.0.1", port=9802)
else:                                               # grain/config stdio plugin (command + args + env)
    mcp.run()
```

Add a background timer that calls `release()` for any `attempt_id` whose `deadline` has passed, and you have a leased-credential capability. For a toolset capability, mount a second FastMCP instance per attempt at `/runs/<attempt_id>/mcp` behind the same middleware and return that path as `mcp_endpoint`; for a proxy, bind a data-plane listener on another port and return it in `ports` with a per-attempt token in `placements` (spec §6).

## What a mint looks like on the wire

Request (`tools/call`): `{"name": "mint_my-method", "arguments": {"attempt_id": "e1010c47-175-2-0", "deadline": "2026-10-19T23:00:00Z", "args": {"account": "staging"}}}`

Response (tool result JSON):

```json
{
  "status": "ok",
  "placements": [
    { "path": "/home/debian/.my-cred.json", "mode": "0600", "content": "{...}" },
    { "path": "/home/debian/setup-root.sh", "mode": "0700", "content": "#!/bin/sh\nset -e\n..." }
  ],
  "mcp_endpoint": "/runs/e1010c47-175-2-0/mcp",
  "ports": [{ "host_port": 19811, "sandbox_port": 9811 }],
  "setup_root": "/home/debian/setup-root.sh",
  "prompt_section": "Credentials are at `/home/debian/.my-cred.json`."
}
```

Any field other than `status` may be omitted. `placements` also take `owner` (default `debian`) and `encoding: "base64"` for binary content. `setup_root` is for the one grant per attempt that initializes the root working directory; a grant with other setup work returns `setup` instead (next section).

## Setting up the sandbox: `setup_root` and `setup`

Two response fields name a command Grain runs inside the sandbox after `ports` are bridged and every grant's `placements` are written, and before any `mcp_endpoint` is wired or the agent CLI / CI steps start (spec §5, §5.5):

- **`setup_root`** initializes the **root working directory** — the directory, created empty, that the agent or CI steps are launched in. Because an attempt has one such directory, **at most one grant per attempt may return a non-empty `setup_root`**; if two do, Grain refuses the run before it boots and names both grants. The canonical user is `#git-repo` with `root: true`, whose script clones the primary repository into its `cwd` (spec §6.3).
- **`setup`** is the same kind of command for everything that is *not* the root: it runs after `setup_root`, in the same directory, and any number of grants may return one. An additional repository is `#git-repo` with `root: false`, whose script clones to `/home/debian/repos/<owner>/<repo>` and whose `prompt_section` says so; a credential capability can use `setup` to sign its CLI in. A `setup` command never initializes the root working directory in `setup_root`'s place.
- Both are the **clean absolute path of an executable** — in practice a script you placed with `mode: "0700"` — executed directly (no shell, no arguments) as `debian`, with `cwd` set to the root working directory, one at a time (`setup_root` first, then each `setup` in grant order), each under a bounded timeout (10 minutes by default). Put the repository, branch, credential file, and target directory *inside* the script, so no `args` value passes through a shell; keep secrets out of its output, which Grain keeps as diagnostics.
- A command that exits non-zero, times out, or cannot be executed aborts the attempt before the agent starts: Grain tears the sandbox down, releases every grant, and reports the grant, exit status, and output tail. On `recreate_sandbox`, Grain re-bridges `ports`, rewrites `placements`, and runs `setup_root` and `setup` again in the fresh VM without re-minting.
- **Every method that can return `setup_root` must also offer an `args` mode in which it does not** (`root: false`), so a run can hold several grants of it — one primary repository and any number of additional ones — without contending for the root.

## Security rules you must keep

- **`args` are untrusted** (they come from repositories, Grainfiles, and agents): allowlist them in `inputSchema.properties.args` (`enum`, anchored `pattern`, `additionalProperties: false`), re-validate in the handler, and never interpolate them into paths, shell commands, SQL, or upstream URLs.
- **`attempt_id` is an identifier, not a secret** (`^[A-Za-z0-9._-]{1,128}$`); for external HTTP servers, the configured bearer token header (`headers`) is the authentication on `/mcp` and `/runs/…` alike, and for stdio servers the control plane is the private stdin/stdout pipe to the child process Grain spawned.
- **Secrets belong in Grain's encrypted secrets store, never in `grain/config`:** bind sensitive `env` and `headers` values via `$secret:<name>` so plain-text `grain/config` exports never contain cleartext credentials (spec §3.1.2).
- **Operator settings belong in `configure`, not `with:`:** projects, accounts, endpoints and credentials are configuration (spec §4.5), which a grant can never override; secret fields are marked `writeOnly: true` and must be top-level strings.
- **Who may use your capability** (teams, repositories, CI events, fork pull requests) is Grain policy, not something you advertise.

## In-tree capability servers (`grain capability <name>`)

The `grain` binary serves seven capability servers of its own over stdio, one per process, for an `mcpServers` entry with `"command": "grain", "args": ["capability", "<name>", <flags>…]` ([`pkg/capability/intree`](../pkg/capability/intree)).

**Built-in registration.** Nobody has to add these entries. Every deployment registers each in-tree server under a canonical, grain-owned capability ID, **`https://grain.dev/capabilities/<name>`**: `…/gcp`, `…/git`, `…/github`, `…/model-proxy`, `…/skill`, `…/host-script` and `…/gchat`. A grant such as `https://grain.dev/capabilities/gcp#gcp-key` therefore means the same thing on every deployment, template and repo. Like any capability ID it is only a name, and nothing ever fetches it.
- **Where the list comes from.** Registration is driven by the same table `grain capability <name>` dispatches from (`intree.register`, `intree.EnsureBuiltins`), so a server added to `pkg/capability/intree` is registered with nothing else to change. A test fails if one has no canonical ID or is missing from the registered set.
- **When it runs.** On daemon start and on every configuration reload, grain adds any missing built-in to `mcp_servers` as `{"command": "grain", "args": ["capability", "<name>"], "builtin": "<name>"}`, enabled and running in the container. It writes the entry to `grain_config`, and from there to the state repo like any other entry, unless grain/config is an external repo the daemon does not write. In that case the entries are applied in memory and appear in grain/config the next time a capability server is saved.
- **Operator settings are kept.** An entry's own settings (`disabled`, `run`, `env`, `config`) are never touched, so a disabled built-in stays disabled. A built-in cannot be renamed or removed, only disabled. `grain capability-servers remove` and Settings both say so.
- **Adoption.** A hand-added entry that runs `grain capability <name>` (or a `run: host` entry naming the hostd entry `grain-<name>`) is adopted: it moves to the canonical ID, and its old ID is kept in `aliases`. Grants naming the old ID keep resolving through the alias, and a one-time `notice` in Settings explains the move. Several entries for one server are merged the same way only when their settings match; one configured, enabled or run differently is left as a second instance under its own ID, so no grant or policy rule naming it reaches settings it did not have. Adoption is a one-time migration: once the built-in is registered, an entry added later that runs the same server under its own ID is a second instance of yours and is left alone.
- **Needs configuring.** Every built-in is registered enabled, and attaches as soon as what it requires is there. Until then it is reported in the **`needs-config`** attach state with the reason, not as a failure. That covers a built-in refused before anybody configured it, or for its configuration, or one that attaches with no methods. It applies to `gcp` before it has a permission set and `host-script` before it has a script, and equally to `skill` (`-dir`), `git` (`repos` and a token) and `model-proxy` (`-vendor` and its key), whose registered entry carries no arguments. Its card offers **Configure**, and **Edit** for the arguments, and it offers no methods until it attaches.
- **Run location.** The default is `container`. `gcp` (ambient ADC on the host), `gchat` (posting as the host user's ambient identity) and `host-script` can also run on the host through grain hostd: `scripts/setup.sh` (and so `google/scripts/cloudtop-install.sh`) writes the allowlist entries `grain-gcp`, `grain-gchat` and `grain-host-script` (`<grain> capability <name>`; `grain-gchat` with `extra_args: true` for its `-listen`), and adds them to an existing allowlist without changing an operator's edits. grain keeps its own entries up to date by itself: `grain-hostd-allowlist.service` (written by `scripts/setup.sh`) runs `grain hostd -ensure-builtins` as root, sandboxed to write only `/etc/grain`, before every hostd start, and the upgrade step that drains hostd onto a new binary starts it too. It adds each entry that build ships (`host-git`, `grain-gcp`, `grain-gchat`, `grain-host-script`, and the edition's own) that the allowlist has no entry of that name for, from one list in Go (`intree.HostdBuiltins`) that the installer's heredocs are tested against. Each addition is logged in that unit's journal. It **never** changes or removes an entry that is already there, even one that differs from the default (the journal says how it differs), never follows a symlinked allowlist, and replaces the file atomically with its owner and mode kept. hostd re-reads the allowlist on every request, so nothing restarts and no run is touched. An entry for a server that is not grain's own is never added. While one of grain's entries is missing, Settings → Capability servers and System → Health say so, with the command to add it now (`sudo systemctl start grain-hostd-allowlist.service`) and the text to paste. An install whose `setup.sh` predates that unit gets it from one re-run of `setup.sh`. A `run: host` built-in that could not start for that reason is retried every minute, so it attaches once the entry is there, with no daemon restart. Its static configure schema lets **Configure** be done before it first starts.
- **No grants.** Registering a server grants nothing: a task still needs a grant naming one of its methods.

**Beside the in-process providers.** The in-process capabilities (`gcp-key`, `self-debug`, the model proxies and the rest, in `pkg/capability/`) keep working unchanged next to their server counterparts. The two are told apart by the grant's form: a bare name such as `gcp-key` is in-process, and a reference starting with `https://` (`model.IsCapabilityRef`) is a server method. A task can hold either or both, and nothing rewrites an old grant into a reference. The grant editors keep them apart: the in-process checklist is **Legacy capabilities**, and the **Grants** section's **Add grant…** menu lists a built-in server's method as `gcp server › gcp-key`. Retiring the in-process providers is a separate, later change. For GCP, read [Running alongside the built-in gcp-key](gcp-key-permission-sets.md#running-alongside-the-built-in-gcp-key) before trialling the server against the same accounts. Every server but `skill` takes its settings through `configure` (spec §4.5), so Settings → Capability servers draws a form for them, with each credential a secret field (`writeOnly`, stored only in the secrets store and referenced as `$secret:<name>`). For `git` and `model-proxy` no field is required: each one given overrides its flag or environment variable, which the entry binds with `$secret:<name>` and which stay the fallback for entries written before `configure`. A server missing its credential from both still starts, with no method, and its attach fails saying what is missing. Settings that bind a listener (`-listen`, `-sandbox-port`, `-vendor`) are flags only, since the listener is bound when the server starts. Every `args` schema is a strict allowlist (`enum`s, anchored `pattern`s, `additionalProperties: false`). `gcp` is configured through `configure` (spec §4.5) with admin accounts and permission sets — see [GCP (`gcp-key`)](#gcp-gcp-key) below; its old flags and `GCP_MINTER_KEY` are migrated to a permission set for entries written before that. Leases live in the server's memory: each is released at `deadline` plus 5 minutes' grace even if `release_` never arrives (`Method.RunExpiry`, checked every 30 s), and every lease still held is released when Grain closes the server's stdin. The in-process providers under `pkg/capability/` stay in use until runs are moved over.

`grain capability host-git` is an internal in-tree server, not a grant: grain hostd starts it on the host so a containerised daemon can use the installing account's gh (or a registered source's tool) login for an ambient git connection. It serves plain tools to the daemon rather than `mint_` methods (see [docs/git-hosts.md](git-hosts.md)).

### `hostd` (capability server set) in the 4-component architecture

In Grain's [4-component architecture](architecture.md#the-4-component-architecture):

1. **`grain` controller (`grain daemon`)** runs the task store, orchestrator, merge queue, UI/API, and sandbox lifecycle.
2. **`grit` controller** runs the CI controller service ([`docs/design/grit-split.md`](design/grit-split.md)).
3. **`model-server` (`grain model-server`, aliases `grain executor` and `grain modelexec`)** runs agent model CLIs (`agy`, `claude`, `codex`) on a host with local ambient credentials/binaries and connects outbound to the `grain` controller.
4. **`hostd` (capability server set, `grain hostd`)** runs on a host to bring up allowlisted capability servers (`grain-gcp`, `grain-host-script`, `grain-gchat`, `host-git`, etc. from `/etc/grain/hostd.yaml`) and connects them back to the `grain` controller — either over the local `/run/grain-hostd` Unix socket when co-located with the `grain` container, or outbound over HTTPS/WebSocket via `grain hostd -server <grain-url> -token-file <path>` (`GET /api/hostd/connect`) when `grain` runs remotely (such as on GKE).

| `<name>` | Method (pattern) | `with:` arguments | Environment | Flags | `configure` fields (secret fields in bold) |
|---|---|---|---|---|---|
| `gcp` | `gcp-key` (§6.1, leased) | `permission_set`: one of the configured sets (an enum, default the first); `primary`: boolean, default `true` (`account` and `activate` are accepted for old grants) | none; admins' configured credentials come through `configure` (`GCP_MINTER_KEY` only for a migrated entry) | none needed (`-project`, `-account [name=]email[*pool-size]`, `-max-key-age` are migrated, deprecated) | `admins`, **`admin_credentials`**, `permission_sets` (see below) |
| `git` | `git-repo` (§6.3, proxy port + working tree) | `repo`: one of the `-repo`s (an enum, or an anchored pattern when one is `owner/*`); `mode`: `read`/`write`, default `read`; `base`, `branch` (anchored ref patterns); `root`: boolean, default `true` | `GIT_REPO_TOKEN`: the upstream credential every proxied request is sent with | `-listen` (default `127.0.0.1:9728`), `-sandbox-port`, `-upstream` (default `https://github.com`), `-repo owner/repo` or `owner/*` (repeatable, at least one), `-push-branch` (anchored pattern, default `^grain/…`) | `upstream`, `repos` (`owner/repo` or `owner/*`), `pushBranch`, **`token`** |
| `model-proxy` | `model-proxy` (§6.3, proxy port) | `vendor`: one of the configured vendors | `MODEL_PROXY_<VENDOR>_KEY` per vendor, e.g. `MODEL_PROXY_CLAUDE_KEY` | `-vendor name[=loopback-address]` (repeatable; `claude`, `openai`, `gemini`; ports from `9729` up) | **`<vendor>Key`** for each `-vendor`, e.g. **`claudeKey`** |
| `skill` | `skill` (§6.4, stateless, no `release_skill`) | `skills`: array of skill names, default all | none | `-dir` (one subdirectory per skill, each with a `SKILL.md`) | none |
| `host-script` | `host-script` (§6.3, proxy port + `grain host-script <name>`) | `script`: one of the configured scripts' names (an enum, required) | `$env:` references a script's `env` makes, bound by the entry to `$secret:` | `-listen` (default `127.0.0.1:9733`), `-data-dir` (default `~/.cache/grain/host-script`) | `scripts` (see [Host scripts](#host-scripts-grain-capability-host-script)) |

What each mint returns:

- **`gcp-key`** mints a per-attempt key for one of the permission set's service accounts (creating it first when the set's pool needs one) and places it at `/home/debian/.gcp-service-account.json` (`0600`) — or, with `primary: false`, at `/home/debian/.gcp-keys/<set>.json` — with a `prompt_section` naming the set, account and project. A primary key also gets `/home/debian/.gcp-key/<attempt_id>/activate.sh` (`0700`) as `setup`, which activates the key in gcloud and selects the project. A pool with every account at GCP's ten keys is `resource_exhausted` with `retry_after`; a `deadline` further away than the set's `max_lease` is `refused`. `release_gcp-key` deletes the key.
- **`git-repo`** authorizes a fresh per-attempt token on the server's git smart-HTTP proxy and returns `ports: [{host_port, sandbox_port}]`, the token in `/home/debian/.git-repo/<attempt_id>/credentials` (`0600`, git's `store` format) and a clone script in `/home/debian/.git-repo/<attempt_id>/setup.sh` (`0700`) — as `setup_root` for `root: true`, cloning into the root working directory, and as `setup` for `root: false`, cloning into `/home/debian/repos/<owner>/<repo>`. With `branch`, the script checks it out from the remote when it exists and from `origin/<base>` (or the default branch) otherwise. The proxy admits only smart HTTP for the granted repository; a `read` grant cannot push, a `write` grant needs a `branch` that matches `-push-branch` (Grain's own `grain/…` task branches by default, so a grant's untrusted arguments cannot name a default or release branch) and is not its own `base`. It can update `refs/heads/<branch>` and nothing else, never delete it, and only fast-forward it: an update of an existing tip is forwarded only when the pushed pack shows the new tip descending from the old one (`gitproxy.PushShowsFastForward`), so a forced push is refused. The upstream sees only the server's own credential.
- **`model-proxy`** mints a token on `pkg/modelproxy`'s proxy for the vendor, valid until `deadline` plus grace, and places it exactly as the in-process `<vendor>-proxy` capability does: `/home/debian/.<vendor>-proxy-url` and `/home/debian/.<vendor>-proxy-token` (`0600`), with the vendor's own `ports` forward. `release_model-proxy` revokes it.
- **`skill`** places each chosen skill's files under `/home/debian/.skills/<skill>/` (`0644`, `0755` for executable files) and a `prompt_section` indexing each `SKILL.md` with its front-matter `description`. Skill and file names are checked when the server starts, so a directory with anything it could not place refuses to start.
- **`config-map`** places operator-configured maps, as Kubernetes ConfigMaps and Secrets do: one named map holds several files, and one grant of that name places them all. A `config_maps[]` entry is `{name, description, dir, data}`, where `dir` defaults to `/home/debian/.config-map/<name>` and `data` maps each key -- a file name matching `[A-Za-z0-9._-]+`, never `.` or `..` -- to `{content, mode, executable, path}`. The file lands at `<dir>/<key>` (`0644`, or `0755` when `executable`), or at `path`, an optional clean absolute override. A `secrets[]` entry is `{name, description, dir, keys}`, with `keys` mapping each key to `{mode, path}` (`0600` by default) and no value. Values live in the write-only `secret_values` field, one encrypted JSON object addressed `"<map>/<key>"`, so a key is set or rotated without resending the others, and a key that is removed loses its stored value. Every path goes through the reserved-path rules (§5.1.2), and no two keys, in one map or across maps, may place the same path. A file is at most 1 MiB and a map at most 8 MiB. A grant names maps (`config_maps`, `secrets`; all of them when it names neither) and may narrow them to `keys: ["<map>/<key>", ...]`, like a volume's `items`. The `prompt_section` lists each map once, with its directory and key names, and never a secret value. The earlier single-file entry shape (`content`/`path`/`mode`/`executable`/`files` on the entry, `secret_values` keyed by entry name) is refused with a message saying how to rewrite it as a map.

- **`host-script`** is described in its own section below.
- An edition linked into grain may register built-in servers of its own (`intree.Register`), documented with that edition; a host-only one runs through grain hostd from the start.

### Host scripts (`grain capability host-script`)

`host-script` runs operator-defined **bash scripts** where the server runs: on the host as the user when its entry is `run: host`, through grain hostd and an allowlist `capabilities:` entry, or in the daemon's container otherwise. A grant makes each script callable in the sandbox through the `grain` CLI, as **`grain host-script <name> [args...]`**, not as an MCP tool. [host-scripts.md](host-scripts.md) is the operator's walkthrough, with an example script that parses its own flags.

- **Configuration.** `configure` takes `scripts: [{name, description, usage?, script, timeout, env?, cwd?, max_output?, max_calls?, max_args?, max_arg_len?}]`, which is operator-only and edited in Settings.
  - `name` matches `^[a-z0-9][a-z0-9_-]{0,62}$` and must be unique.
  - `timeout` is in whole seconds, from 1 to 86400.
  - The limits default to 1 MiB of each output stream, 1000 calls per grant, 256 arguments and 64 KiB per argument.
  - Script text exists only here: no grant and nothing in a sandbox can supply or alter it.
  - `env` holds fixed variables. A value `$env:NAME` takes the server's own variable `NAME`, which the entry's `env` binds to `$secret:<name>`. Secrets are written that way because `configure` accepts a `$secret:` reference only as the whole value of a top-level field (spec §4.5.2).
- **Grant.** `…/host-script#host-script` with `with: {script: <name>}`, an `enum` of the configured names. A task may hold several, one per script. Two grants of the same script would place one descriptor path twice, so the pass is refused naming both (§5.1.2).
- **Mint.** The server returns:
  - its data-plane port in `ports`;
  - the grant's descriptor at `/home/debian/.host-script/<name>.json` (`0600`): the script's `name`, `description` and `usage`, the port, the `attempt_id` and the grant's token, one field to a line;
  - a stand-in `grain` at `/home/debian/.host-script/grain` (`0755`) and a `setup`, `/home/debian/.host-script/install-grain` (`0700`), which copies it to `/home/debian/.grain/bin/grain` only when the sandbox has no `grain` of its own that runs `host-script`. Both are identical in every grant, so a pass with several places and runs them once each (§5.1.2);
  - a `prompt_section` naming the script with its `description` and `usage`, and how to call it: `grain host-script <name> [arguments...]`, `grain host-script <name> --grain-help`, arguments passed literally without a shell and logged.

  `grain host-script <name> [args...]` (`pkg/hostscriptcli`; the full `grain` binary and the sandbox image's own `/usr/local/bin/grain`, built from `cmd/grain-sandbox` into `Granulefile.guest-base`, answer it the same way) reads the descriptor, POSTs its argv hex-encoded one per line (`Content-Type: text/vnd.grain.argv-hex`) to `http://127.0.0.1:<port>/runs/<attempt_id>/exec` with `Authorization: Bearer <token>`, replays the `o ` frames to stdout and `e ` frames to stderr as they arrive, and exits with the `x <status>` frame's status, or 125 when there is no grant, the token is refused or the endpoint does not answer. It doesn't forward stdin. `grain host-script <name> --grain-help` (or `grain host-script --help <name>`) prints the description and usage locally, and `grain host-script --list` lists every script granted to the sandbox. The stand-in is a POSIX `sh` + `curl` + `od` script that does the same for images built without a `grain`. `/home/debian/.grain/bin`, the one placement directory allowed under `/home/debian/.grain` (spec §5.1.2), is first on the `PATH` of `run_command` and grain CI steps.
- **Execution.** The server runs `bash <private 0700 file> "$@"` with the caller's argv as an array, never a shell string, so spaces, quotes, `$(…)`, backticks, `;`, newlines and a leading `--` all arrive literally. Scripts must treat their arguments as untrusted.
  - A call is refused with exit 125 for a missing or wrong token, a NUL byte, too many or too long arguments, or `max_calls` used up.
  - The script's environment is only its `env`, `GRAIN_TASK_ID`, `GRAIN_ATTEMPT_ID` and the ambient identity variables (`HOME`, `USER`, `LOGNAME`, `SHELL`, `PATH`, `LANG`/`LC_*`, `TZ`, `XDG_*`, `DBUS_SESSION_BUS_ADDRESS`, `SSH_AUTH_SOCK`, `KUBECONFIG`, `CLOUDSDK_CONFIG`, `GOOGLE_APPLICATION_CREDENTIALS`, `AWS_PROFILE`, `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `DOCKER_HOST`). Under `run: host` those come from the user; in the container, from the container.
  - At `timeout` the script's process group gets SIGTERM, then SIGKILL two seconds later, and the call exits 124.
  - Each stream is capped at `max_output`, with a truncation marker.
  - The default `cwd` is a scratch directory private to the grant.
- **Testing from Settings.** The server also advertises a control-plane tool of its own, `test_script` (`{script, argv}`), which attach status reports under `tools` and `POST /api/capability-servers/test-script` calls: it runs one configured script once in a throwaway grant (`attempt_id` `settings-test-<hex>`, which no task attempt can be), its timeout capped at a minute, and answers with stdout, stderr, exit code and duration. Its `configure` schema marks `usage` `format: "textarea"` and `script` `format: "code"`, which Settings draws as multi-line fields, and `env` is a string map drawn as name/value rows ([host-scripts.md](host-scripts.md#3-try-it)).
- **Audit.** Every call, refused ones included, is recorded with script, **argv**, exit code, duration and `caller: sandbox` (`settings-test` for a Settings test), in the server's log and in `<data-dir>/audit.jsonl`. The release returns the grant's records to Grain (spec §4.4.4), which stores them on the attempt's run record and lists them in the task's attempt view, so anyone who can read the task can read them. Arguments are logged verbatim in all three places, so never pass a secret as one. A record keeps at most 64 arguments of 1 KiB each (`argv_truncated` says when it cut). Refused calls are recorded up to `max_calls` per grant, then one record says the rest go unrecorded, so a sandbox can't grow the log without bound.
- **Release** revokes the token, kills calls still running, waits up to 10 seconds for them to record how they ended, and removes the grant's directory. The command then exits 125, saying the grant was released.

A grant's ports are forwarded into the sandbox through the deployment's capability port relay (§5.3), and a mint returning ports is refused where there is none. The servers can also be exercised directly over stdio, or with `protocol.Attach`.

## Google Chat (`gchat#post`)

`grain capability gchat` lets agents post to Google Chat spaces an operator allowlisted, as a bot, **one way**: nothing said in a space comes back into grain, and there is no read tool. It is a built-in (`https://grain.dev/capabilities/gchat`), registered enabled and reported as needing configuring until it has a space. The operator how-to is [docs/gchat.md](gchat.md).

- **Configuration** (`configure`, edited in Settings → Capability servers). `spaces`: `[{name, transport, space?, app?, display_name?, avatar_url?, allowed: {threads, max_len, mentions, per_hour}}]`. `transport: webhook` posts through the space's incoming webhook, whose URL goes in the secret field **`webhook_urls`** (a JSON object from space name to URL); it needs no Chat app approval. `transport: app` posts through the Chat API as a Chat app to `space` (`spaces/…`), under one of `apps: [{name, credential_source, project?}]`. Credentials are described by source (spec §4.5.8): `configured` — a service-account key or workload-identity config with the `chat.bot` scope, in the secret field **`app_credentials`** (a JSON object from app name to credential); `ambient` — nothing is stored and the server uses its own process's ADC, which is how Google-internal Chat works when the server runs on the host through hostd. Also `grain_url` (required: messages link to `<grain_url>/tasks/<id>`), `display_name` and `avatar_url` (the deployment defaults for the card header), `per_attempt`, and the secret **`dataPlaneToken`**, which grain generates and keeps itself (an override only). Internal and external spaces sit side by side, each tied to its own transport and credential source.
- **Grant.** `https://grain.dev/capabilities/gchat#post` with `with: {space: <name>}`, an enum generated from the configured spaces: a grant can never name an unconfigured space or a URL. Grant one per space.
- **Toolset** (`mcp_endpoint`). `post_message(text, thread?)` returns `{message, thread, space}`. Each task gets its own thread per space, keyed by task id (`threadKey` `grain-<deployment>-task-<id>`), so every attempt of a task posts into the same thread. `thread: "new"` starts another, and a thread id `post_message` returned replies into it, only where the space sets `allowed.threads`.
- **Guardrails, enforced by the server.** Per-attempt (`per_attempt`, default 20) and per-space hourly (`allowed.per_hour`, default 60) rate limits; `allowed.max_len` (default 2000, at most 4096); Google Chat text formatting only (Markdown links, `**bold**`, `#` headings and HTML are refused with the Chat equivalent); no `<users/…>` mentions unless `allowed.mentions` is `users` or `all`, and no `@all` unless it is `all`; every message stamped with its task id and a link to the task.
- **Bot name.** Every message carries a card header with the space's `display_name` (else the deployment's, else `grain`) and optional `avatar_url`. A webhook's own sender name and avatar are set when the webhook is created in Chat and cannot be changed per message; an app posts under the Chat app's configured name.
- **Test and usage.** Configure reports each app's resolved identity (and its kind: `service_account`, `external_account`, `authorized_user`, `metadata`) and whether it can reach each of its spaces, which Settings' **Test** shows. The control-plane tools `test_post({space})` (posts a message marked `[grain test]`) and `usage` (per space: posted, refused, failed, last hour) are there for Settings: the gchat card lists each space with its usage (`GET /api/capability-servers/gchat-usage?id=`) and a **Send test message** button (`POST /api/capability-servers/gchat-test` `{id, space}`).
- **Release and audit.** `release_post` closes the attempt's endpoint and returns every post and refusal of the attempt, with its text and message id, as the release's audit (spec §4.4.4). The server logs space, outcome and ids, never a webhook URL, key or token.
- **Where it runs.** In the container (configured credentials, or the container's ambient ADC), or on the host through hostd as `grain-gchat` (`run: host`, the user's ambient identity). Both are tested (`pkg/capability/intree/gchat_hostd_test.go`).

## GCP (`gcp-key`)

The `gcp` server ([`pkg/capability/gcpsets`](../pkg/capability/gcpsets), served by [`pkg/capability/intree/gcp.go`](../pkg/capability/intree/gcp.go)) gives a task a GCP service-account key with exactly the roles an operator chose for it. The operator's how-to is [gcp-key-permission-sets.md](gcp-key-permission-sets.md).

**Credentials are described by source.** *Configured*: a secret in grain's store, given to the server through `configure` — a service-account key JSON or a workload-identity external-account config. *Ambient*: the identity the server process already has where it runs — on a host through `grain hostd` (`run: host`) that is the user's own gcloud identity; in a container it is the container's ADC or metadata-server identity.

**Configuration** (`configure`, edited in Settings → Capabilities → Capability servers):

- `admins`: `[{name, credential_source: configured | ambient, projects[], role_ceiling[]}]` — the identities that create service accounts, bind roles and mint keys. `role_ceiling` is every role the admin may grant; a permission set cannot exceed it.
- `admin_credentials` (write-only): a JSON object from admin name to its configured credential, `{"minter": {…key or external-account config…}}`. grain keeps a secret only as a whole top-level field, so every configured admin's credential lives in this one field. An ambient admin must not have an entry.
- `permission_sets`: `[{name, admin, project, roles[], resource_scope?, max_lease?, pool_max?, adopt_legacy?}]` — one project per set; `resource_scope: buckets/<name>` binds the roles on that bucket instead of the project; `max_lease` (default `24h`) caps a key's life; `pool_max` (default 3) caps how many service accounts the set may create. `accounts[]` instead names existing accounts to mint keys for without creating or binding anything — what migration produces. `adopt_legacy: true` also uses the set's accounts from before names carried the deployment id (`grain-<set>-<n>`, marked `[grain-gcp-key set=<set>]`); it is on for migrated sets and off otherwise.
- `account_prefix` (default `grain`): letters and digits, at most 10, starting every account name.
- `grainDeploymentId`, `grainInstanceId`: not in the form. Grain fills them with the deployment id its branch names use (`Store.DeploymentID`) and its installation id on every `configure` (spec §4.5.7). A set that creates accounts is refused without the deployment id.
- `delete_orphans`: names of removed sets whose service accounts should be deleted. Nothing is deleted unless it is named here.

**Grants** choose a set with `with: {permission_set: <name>}`, an `enum` built from the configured sets, so the grant form shows a dropdown; a grant with no arguments gets the first set. A task may hold grants of several sets: give all but one `primary: false`, so each key gets its own file and only the primary is activated.

**Service accounts on demand.** The first mint for a set creates `<prefix>-<deployment id>-<set>-<n>@<project>`, e.g. `grain-049f1e09-ci-1` (`n` from 1, skipping a name another account already has). When that is longer than GCP's 30 characters, the set's name is cut first, keeping four hex digits of the hash of `<project>/<set>`, and then the deployment id, keeping four hex digits of its own hash, so the id is deterministic and always valid (`gcpsets.AccountID`). The set's roles are bound to it. Every account it creates carries `[grain-gcp-key deployment=<deployment id> instance=<installation id prefix> set=<set>]` in its description. That marker is the only thing that makes an account this deployment's: listing, minting, reaping, Test and `delete_orphans` consider only accounts with this installation's id in it. So deployments sharing a project never touch each other's accounts, and a renamed deployment id keeps its accounts while naming new ones under the new id. Accounts and bindings without the marker are never changed or deleted, and the only IAM members it adds or removes are its own accounts. Naming, renames and old accounts are covered in [gcp-key-permission-sets.md](gcp-key-permission-sets.md#account-names-and-ownership). Each `configure` and each mint reconciles drift: the set's accounts hold exactly the set's roles, on its scope, and nothing else.

**Pool and quota.** GCP allows ten user-managed keys per service account. A mint uses the set's account with the fewest keys, creates the next account when every one is full and the set is under `pool_max`, and is `resource_exhausted` otherwise.

**Keys.** A mint creates one key per attempt; release deletes it; the server releases a lease at its deadline plus 5 minutes' grace if release never comes, and every 10 minutes a reaper deletes any key on the capability's own accounts older than the set's `max_lease` plus grace that no lease holds (keys left by a restarted server).

**What the agent is told.** A key is placed at `~/.gcp-service-account.json` (or `~/.gcp-keys/<set>.json` for `primary: false`) with a `.about` file beside it (`~/.gcp-service-account.about`) naming the permission set, the service account, its project, its roles and the key's path. The prompt names the same, so an agent can check it holds the credentials it was meant to get.

**Admin credentials and failures.** An admin's credentials are built once and kept for later mints, so their token fetches are never tied to the call that built them; each fetch has its own 20-second bound (`gcpsets.TokenTimeout`), and a fetch that is cancelled or times out is retried once. A mint that fails says which failure it is:
- *Missing* ambient credentials (no ADC where the server runs) or a configured credential that does not load: refused, saying to run `gcloud auth application-default login` on the host as the user grain was installed as (or to replace the configured credential).
- *Expired* credentials (Google answers `invalid_grant`, `invalid_rapt` or `invalid_client`: a refresh token expired, revoked or needing reauthentication, or a deleted key): refused with the same instruction. The admin's credentials are read afresh on the next mint, so logging in again is enough.
- *Timeout*: a token fetch cancelled or timed out even on its retry is a transient error.
- *IAM denied* (403): refused, naming the admin and the project; Test lists the missing permissions.

**Test.** Every `configure` — Settings' Test button included — puts a report in the `gcp-key` method's description: which identity each admin resolved to (an ambient admin especially) after fetching its access token the way a mint does, so stale credentials show here first with the same message, the permissions `testIamPermissions` says it lacks on each project, each set's accounts against `pool_max` and keys held now, and the accounts of removed sets, with how to delete them. It starts with the deployment id and how new accounts are named, and ends with how many accounts in each project belong to other grain deployments, or carry the old marker and are adopted by no set; those are counted only.

**What an admin needs.** On each project in `projects`, this custom role (and, for a set with a bucket `resource_scope`, `storage.buckets.getIamPolicy` and `storage.buckets.setIamPolicy` on the bucket):

```yaml
# gcloud iam roles create grainGcpKeyAdmin --project=PROJECT --file=grain-gcp-key-admin.yaml
title: grain gcp-key admin
description: Create grain's per-set service accounts, bind their roles and mint their keys.
stage: GA
includedPermissions:
- iam.serviceAccounts.create
- iam.serviceAccounts.delete
- iam.serviceAccounts.get
- iam.serviceAccounts.list
- iam.serviceAccountKeys.create
- iam.serviceAccountKeys.delete
- iam.serviceAccountKeys.list
- resourcemanager.projects.get
- resourcemanager.projects.getIamPolicy
- resourcemanager.projects.setIamPolicy
```

`resourcemanager.projects.setIamPolicy` lets the admin write any binding; `role_ceiling` is what keeps grain to the roles you listed. To hold the admin itself to them too, grant the role with an IAM condition limiting `setIamPolicy` to those roles (`api.getAttribute('iam.googleapis.com/modifiedGrantsByRole', []).hasOnly([...])`). A later version minting short-lived tokens instead of keys will also need `iam.serviceAccounts.getAccessToken` (Service Account Token Creator) on the sets' accounts.

**Migration.** An entry still configured by `-project`, `-account` and `GCP_MINTER_KEY` (or by the old `project`, `accounts`, `minterKey`, `maxKeyAge` fields) is migrated on start (`gcpsets.Migrate`): the minter key becomes the configured admin `minter`, and each pool becomes a set over exactly its old accounts — the one called `default` becomes `sandbox-agent` — which the server mints keys for and never modifies or reaps. Existing argument-less grants get the first set, and `with: {account: <name>}` still names a set.

## Expose it in Grain (`grain/config` `mcpServers` & Settings UI / CLI)

Register capability servers in `grain/config` (or through the Settings UI / CLI: `add`, `remove`, `list`, `enable`, `disable`) under `mcpServers` following Antigravity (`agy`)'s `mcp_config.json` shape ([docs/agy-surface.md](agy-surface.md), spec §3.1) — **stdio** (`command`, `args`, `env`) for in-tree capabilities and `grain/config` plugins (relative `command` paths resolved from the root of `grain/config`), and **HTTP** (`serverUrl`, `headers`) for external capability servers, with secrets bound via `$secret:<name>` in Grain's encrypted secrets store:

```json
{
  "mcpServers": {
    "https://github.com/ai-on-gke/grain/capabilities/gcp": {
      "command": "grain",
      "args": ["capability", "gcp"],
      "config": {
        "admins": [{"name": "minter", "credential_source": "configured", "projects": ["my-project"], "role_ceiling": ["roles/storage.objectViewer"]}],
        "admin_credentials": "$secret:gcp-admin-credentials",
        "permission_sets": [{"name": "reader", "admin": "minter", "project": "my-project", "roles": ["roles/storage.objectViewer"]}]
      }
    },
    "https://github.com/my-org/capabilities/custom-plugin": {
      "command": "plugins/custom-plugin/server",
      "args": ["--mode", "prod"],
      "env": {
        "PLUGIN_API_KEY": "$secret:custom-plugin-key"
      }
    },
    "https://github.com/my-org/capabilities/external-cap": {
      "serverUrl": "http://127.0.0.1:9802/mcp",
      "headers": {
        "Authorization": "Bearer $secret:external-cap-token"
      }
    }
  }
}
```

In this build the map is `grain_config.mcp_servers` (exported to `tables/grain_config.json` in `grain/config`), and it is edited in **Settings → Capabilities → Capability servers** — add, test, configure, edit in place, set the run location and see who grants a server, with **Capability policy** beside it; [capability-servers-ui.md](capability-servers-ui.md) walks through it — or with `grain capability-servers list|add|remove|enable|disable|bind-secret`. For example:

```sh
grain capability-servers add https://github.com/my-org/capabilities/external-cap --url http://127.0.0.1:9802/mcp
grain capability-servers bind-secret https://github.com/my-org/capabilities/external-cap \
  --header Authorization --prefix "Bearer " --secret external-cap-token --value-stdin < token.txt
```

The daemon attaches each enabled server within a tick of the change (`pkg/capability/servers`). It refreshes `tools/list` on every configuration reload and hourly, and retries a failed attach every minute, so a secret added afterwards is picked up without editing the entry. Settings shows each server's state, discovered methods and attach error. That error names a missing secret, never its value. A server that advertises `configure` also takes a `config` object in its entry, validated against its configure schema and saved through `PUT /api/capability-servers` (secret fields as `configSecrets`, stored only in the secrets store); `POST /api/capability-servers/test` runs `initialize`, `tools/list` and `configure` on an entry before or after saving it, and `GET /api/capability-servers/host-entries` says whether `run: host` is possible here (spec §4.5). `grain state check` warns about an entry the daemon would refuse and about a credential-looking `env`/`headers` value committed in cleartext.

Tasks, templates, schedules, Grainfiles, and `.grain/workflows` CI jobs can then request `https://github.com/my-org/capabilities/custom-plugin#my-method` (or the bare URL when the last path segment names the method), with `with:` arguments validated against your schema. When a grant returns an `mcp_endpoint`, Grain proxies the attempt's tools and wires the server into the active agent CLI's native per-run configuration alongside `grain-sandbox` (spec §5.2.4):
- **Antigravity (`agy`):** `$HOME/.gemini/config/mcp_config.json` (`mcpServers.<name>` with `timeoutSeconds` and `tools: {"<tool>": {"eager": true}}`), `$HOME/.gemini/antigravity-cli/settings.json` (`permissions.allow` `"mcp(<name>/<tool>)"`), and `HookDecision` admission.
- **Claude (`claude`):** per-run `--mcp-config` (`mcpServers.<name>`) plus `mcp__<name>__<tool>` in `--allowedTools`.
- **Codex (`codex`):** per-run `$CODEX_HOME/config.toml` under `[mcp_servers.<name>]` (`url`, `http_headers`, `required = true`, `tool_timeout_sec`).
- **Custom agent backends & CI containers:** `mcp_servers` in the custom backend JSON launch request ([docs/custom-agent-backends.md](custom-agent-backends.md)), and `/home/debian/.grain/mcp.json` in `.grain/workflows` CI containers.

### Sandbox-first delivery (`grain cap`) and `modelOnly` tools

Under the sandbox-first agent surface ([docs/design/sandbox-first-agent-surface.md](design/sandbox-first-agent-surface.md), spec §5.2.6), capability delivery separates **sandbox-callable tools** from **model-only tools**:
- **Default for external capability servers (`modelOnly: true`):** Tools served on `/runs/<attempt_id>/mcp` by an external capability server default to `modelOnly: true` (wired only to the host-side agent CLI's MCP channel above, and refused with HTTP `403` on the sandbox proxy) so untrusted processes inside the sandbox (`go test`, package install hooks) cannot invoke them unless the server explicitly opts in.
- **Opting into sandbox CLI delivery (`grain cap <server> <tool>`):** Set `"annotations": {"modelOnly": false}` (or `"x-grain-model-only": false`) on a tool in `tools/list`, or `"annotations": {"mcpDelivery": "sandbox"}` on `mint_<method>` (with `"modelOnly": true` on any individual tool that must stay model-only). Tools with `modelOnly: false` are proxied at `http://127.0.0.1:9728/cap/<name>/mcp` (and listed in `/home/debian/.grain/mcp.json` for both task runs and CI jobs), invoked from the sandbox via `grain cap <name> <tool> [flags | --json '{...}']` with no MCP result-size cap, and introduced to the agent through `prompt_section`.
- **Built-in tool capabilities and dangerous scopes:** Grain's own tool-serving capabilities (`self-debug`, non-dangerous `api:<scope>` grants, `grain-log`, `deployment-health`, `bootstrap-playbooks`, `memory`, `release-notes`, `read-issues`, `quarantine-test`, `db-read`) become scopes on the per-attempt sandbox CLI credential (`grain ls/read/grep`, `grain api`, `grain log`, `grain health`, `grain memory`, …) and are revoked when the attempt ends. Dangerous API scopes (`tasks:admin`, `merge:force`, `settings:write`, `connections:write`, `secrets:write`, `capabilities:admin`, `deployment:upgrade/restart/reboot`) and host/queue control capabilities (`dispatch-control`, `pipeline-control`, `self-upgrade`, `self-repair`, `db-write`, `team-broadcast`) stay strictly model-only and are never placed on the sandbox credential.
- **Testing your capability server's delivery:** Run **Test** in **Settings → Capabilities → Capability servers** (`POST /api/capability-servers/test`) to validate `initialize`, `tools/list`, `configure`, and see each method's resolved delivery mode (`delivery`: `sandbox` vs. `model_only`), which applies to every tool its endpoint serves unless the tool sets its own `modelOnly`. Inside a sandbox, run `grain cap list` to verify which tools are exposed to the sandbox CLI (and which are model-only), `grain cap <name> <tool> --help` to inspect generated flags, and `grain cap <name> <tool> --json '{...}'` to test end-to-end execution and `release_<method>` revocation.

## Granting from the UI

Every place the UI edits grants -- the **New task** overlay, the task pane, and the template and schedule editors -- has one **Grants** section for grain API scopes and presets and capability server methods with their `with:` arguments ([design/unified-grants.md](design/unified-grants.md)). In-process capabilities (`gcp-key`, `self-debug`, ...) stay a separate checklist, **Legacy capabilities**, until each is converted.

- **Add grant…** is one search over everything that can be granted, grouped by kind: Grain API presets, Grain API scopes by area (a scope can be limited to one repository with **Grain API grants apply to**), then capability server methods. It re-reads the attached servers' methods every time it opens. Server methods are listed as every method the attached servers advertise. A method without arguments is granted straight away. So is a method with arguments whose defaults already pass its schema (`mint_<method>`'s `inputSchema.properties.args`); one with a required argument still to fill in is added as a draft section, with its form inline, and is granted as soon as what is typed into it passes the schema's check. There is no separate dialog and no **Grant** button.
- Every grant is a section in one list. A grant with nothing to set is a single line: an API grant reads `api:ci:write` (or `api:ci:write on acme/widgets`) with its summary, badged `dangerous` or `no longer offered`; a method without arguments reads its server and method, for example `github.com › gcp-key`, badged when its server is gone. Its tooltip has the whole grant. **×** removes it.
- A method grant with arguments is a section showing the method, its reference and description, and its argument form inline, so its options are always visible and edited in place. It saves itself like the rest of the task pane: an edit that passes the live check is saved half a second after the last keystroke, or at once when focus leaves the section, replacing the grant it edits; one that does not is marked against its field and not saved. A refusal from the server is shown the same way. A section is flagged when its server is gone, its arguments no longer validate, or the server now normalizes them differently. **×** removes the grant, or discards a draft that was never granted.
- **Add another**, on a method's last section, grants the same method again with different arguments (two `git-repo` grants, a key for both staging and prod). Equal grants are one grant (spec §2.3.2), so adding one the task already holds changes nothing.
- On a running task a change applies on the next run, as before.

The form renders exactly the keywords grain enforces (spec §4.2.1):

| Schema | Field |
|---|---|
| `enum`, `const` | a select |
| `type: boolean` | a switch |
| `type: string` with `pattern`, `minLength`, `maxLength` | a text field, checked as you type |
| `type: integer` / `number` with `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum` | a number field, checked as you type |
| `type: array` with `items`, `minItems`, `maxItems`, `uniqueItems` | an add/remove list |
| `type: object`, including through a local `$ref` into `$defs` | a nested group |
| `oneOf` / `anyOf` | a picker for which option, then that option's field |

A property's `description` is its help text, its `default` is its starting value, and `required` properties are marked with `*`. A string with `format: "textarea"` is a multi-line field, and `format: "code"` one in a monospace font. An object with no `properties` whose `additionalProperties` is a plain string schema is a string map, edited as name/value rows with the schema's first `examples` value as the placeholder. If a method's schema uses anything else (`allOf`, `not`, any other schema-valued `additionalProperties`, a `$ref` outside the document), the form does not guess. That grant is edited as a JSON object instead, and the server checks it. The form never drops a field.

The live checks are a convenience. The server normalizes every grant before it is kept: `POST /api/capability-grants/check` for a form that has not been saved yet (new task, template, schedule), and `POST /api/tasks/{id}/capabilities` for a task. Both run the same validation and normalization as task creation. A refusal comes back with a `fields` map keyed by argument path (`project`, `scopes[1]`, `labels.team`; `""` for the arguments as a whole), and the form shows each message against its field.

The UI also checks each recorded grant against the server's current schema (spec §2.3.1). A grant is flagged, and not rewritten, when its server is no longer attached, when its recorded arguments no longer validate, or when the server would now normalize them differently (for example, a default added since it was granted). The task keeps what it was granted until someone edits it.

`with:` arguments are recorded in plain text on the task and shown in its pane. **They must never carry secrets.** The form says so, and it does not offer a field that looks like one: a property named like a secret (`token`, `password`, `secret`, `api_key`, `credential`, `private_key`, ...), or one marked `writeOnly` or `format: password`. A method that *requires* such a field cannot be granted from the UI. Put the server's credentials in its `env`/`headers` through `$secret:<name>` instead (above).

Approval rules are unchanged. The owner's own edits in the UI apply directly, as before. A grant an agent proposes for a child task still needs approval unless the parent's grants cover it (spec §2.3.4).

**Settings → Capabilities → Capability servers** lists, for each attached server's methods:

- the method's description;
- whether it is **stateful** (it advertises `release_<method>`, so what it mints is released at teardown) or **stateless**;
- its arguments: name (nested ones dotted), type, whether it is required, its default, and the values or pattern it allows.

The API behind this:

| Route | What it does |
|---|---|
| `GET /api/capability-methods` | Each attached method: `ref`, `capability`, `method`, `description`, `args` (the normalized schema), `requiresArgs`, `releasable`, and `policy` (any capability-policy verdict per run kind: `"denied"` or `"may_be_refused"`). With `?kind=task` (or `process`, `ci`) and `&repo=owner/name`, each method also carries `policyRefusal`: why the capability policy denies it that one run, empty where it admits it. `/api/config`'s `capabilityMethods` is just the count. |
| `POST /api/capability-grants/check` | `{"grants": [{"capability", "with"} or {"grant"}]}` returns, for each grant, `recorded`, `args`, `error`, `fields`, `changed` and `unchecked`. It writes nothing. |
| `POST /api/tasks/{id}/capabilities` | `{id, attach}` as before, or `{capability, with, attach}`. `{grant, attach: false}` removes one recorded grant exactly. `replace` on an attach swaps one grant for the new one in place. A grant nothing offers any more can still be removed. |

`grain demo` attaches a real capability server for trying this out: `https://capabilities.demo.grain.example/cloud`, with a `cloud-key` method that has required, enum, pattern, bounded, boolean, array and nested arguments, and a `cloud-viewer` method with no arguments.

## Priority dispatch (`priority-dispatch`)

A task granted `priority-dispatch` is dispatched the way a console
session is. It is meant for work whose job is to manage a congested
pipeline, such as the pipeline steward (`docs/pipeline-steward.md`),
which would otherwise be starved by that congestion. Such a task:

- starts whenever it is ready, even with every worker busy: it isn't
  counted against or held by `max_workers` or the merger count. It
  still needs room in the sandbox budget, which it is charged under the
  same exception policy a session's sandbox is;
- is ordered ahead of merge-queue repairs and ordinary work, alongside
  console sessions;
- passes a pipeline pause, a manual one included. The exception is an
  upgrade's own hold, whose restart would end the run;
- is never linked to wait on a red base branch's repair;
- isn't held by a model connection's concurrency limit, and takes no
  slot of it;
- after a run that never got past setup (`setup-failed`: the sandbox
  couldn't be prepared), is retried within two minutes rather than
  backing off up to half an hour. The failure still counts toward the
  task's consecutive-failure limit.

It places nothing in the sandbox and serves no tool. Its whole effect is
on dispatch, through one predicate, `model.Task.HasPriorityDispatch`,
which is true for console sessions and for every holder of this grant.
The store's queries use the same predicate in SQL.

**Only a person may grant it,** because it lets a task jump the queue.
Grant it from a task's, template's or schedule's capability checklist,
or through `POST /api/tasks/{id}/capabilities`, as a person. An agent
can't grant it to anything, even when its own task holds it: a request
body naming it is refused, a `propose_task` naming it is filed for a
person to approve, and a template carrying it can't be named by an
agent. grain's own pipeline steward template is the one exception,
because it carries the grant by construction.

## Durable (`durable`)

A task granted `durable` keeps going until its work is done rather than
until a clock runs out (`docs/design/durable-tasks.md#DT-1`). It implies
`priority-dispatch`, so everything above applies to it too. Only a person
may grant it, for the same reason. In addition:

- its runs have **no wall-clock deadline** and no wait-credit cap. The
  prompt says so instead of stating a budget. Its capability leases are
  minted for 24 hours and renewed while the run lasts;
- an **idle watchdog** replaces the deadline. A run with no transcript
  output, no tool call and no declared wait in progress (`wait_for_checks`,
  `wait_for_task`, a CI job, a chat wait) for 60 minutes
  (`orchestrator.Config.DurableIdleTimeout`) is suspended with kind
  `idle` and resumed on the next cycle;
- it is held to its own ceiling, `orchestrator.Config.MaxDurableRuns`
  (default 4), rather than `max_workers`. A durable task the full ceiling
  holds back records why on its row and logs `durable.ceiling:<team>`.
  That log becomes an operator notification once DT-S6 exists;
- a **setup failure is a suspension** of kind `setup`, not a strike. It
  is retried after two minutes, and the fifth in a row logs
  `durable.setup-failing:<task>`.

A durable run holds a sandbox for as long as it lasts.

## The agent API (`api:<scope>`)

Grain's own routes are a capability too. A task granted `api:<scope>` or
`api:<preset>` is served one `api_*` tool per route of grain's REST API
under that scope. Each tool calls the same handler the UI does. Append
`@<owner>/<name>` to limit a grant to one repository.

Scopes marked dangerous (`capabilities:admin`, `secrets:write`,
`settings:write`, `connections:write`, `merge:force`, `tasks:admin`,
`deployment:*`) are a person's to grant. An agent can't pass one on
through `propose_task` or any route.

A CI job can declare the same grants in its workflow's `capabilities:`.
There they are worth a per-job token for grain's CI API listener,
narrowed to the job's own repository, on trusted events only, and never
for a dangerous scope ([docs/agent-api.md](agent-api.md#ci-jobs)).

[docs/agent-api.md](agent-api.md) covers the vocabulary, the presets, the
policy and the rails. `self-upgrade` and `pipeline-control` are now
aliases for presets of the same names, and keep their own tools.
