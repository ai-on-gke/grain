# `shibu`, the reference CLI, v1

*Kernel* ([design, CLI parity](../shibu.md#cli-parity)). `shibu` is the
kernel's reference client of its API. Each command is a sequence of the
operations in [`kernel-api.md`](kernel-api.md) and
[`agents.md`](agents.md), made as one namespace; the few
[convention commands](#convention-commands) also call a convention
endpoint such as `requests`. It is also the
standard the [`ui/v1`](extension-points.md#uiv1) contract is tested
against: **anything a UI can do, the CLI can do.**

## Where it runs and who it is

- **Inside a VM**, it calls `$SHIBU_ENDPOINT` and is the VM's namespace.
  It needs no login and has no credential. `--as` is refused there.
- **Outside**, `shibu login` authenticates the person at the front door
  ([`web-proxy.md`, The front door](web-proxy.md#the-front-door)), and
  `--as <path>` (or `SHIBU_AS`) picks the namespace to act as, sent as
  `Shibu-Act-As`. Without it, the person's `/users/<name>`.
- `--output json` prints the API's responses as they are, for scripts.
  The default output is text.
- Paths may be relative to the acting namespace, as in the API
  ([`README.md`, Names and paths](README.md#names-and-paths)).

## Commands

### People and identity

| Command | Calls |
| --- | --- |
| `shibu login [--server <url>]` | the front door's device flow |
| `shibu whoami` | `GET /_shibu/v1/whoami` |
| `shibu --as <path> <command>` | the command, as that namespace |

### Agents and chat

| Command | Calls |
| --- | --- |
| `shibu agent create <path> --adapter <name> --model <model> --job <text \| @file> [--runs-in <runner>] [--tools <vm>/mcp[:ops] …] [--payer <alloc>] [--credential <secret>] [--budget usd/day=20] [--regrant-launch] [--grant …] [--keep 5Gi] [--lease 8h] [--title …] [--dry-run]` | The standard distribution's `create_agent` helper, run in the CLI as the caller: one `batch` (`create_namespace` with `agent`, the VM, the grants), then `POST <runs-in>/launch`, then `delete` if the launch is refused ([`agents.md`, `create_agent`](agents.md#create_agent)). `--credential` is a reference the runner reads through its own `read` grant. Without `--runs-in` and `--tools`, it creates a VM in `<path>` (charged to `--payer`) and runs there (`same-vm`). `--runs-in /sys/runner --tools none` is a planner with no VM. `:ops` is `exec,read,write` by default, e.g. `--tools build/b2/mcp:read` |
| `shibu agent runs-in <agent> <runner>` | `POST <agent>/agent/runs_in`, between sessions only |
| `shibu runners [path] [--adapter <name>]` | `list(path, selector=endpoint:runner, include_grants=true)`, then `GET <runner>/capacity` and `GET <runner>/adapters` on each: the runners the caller can `launch` on, their free sessions, isolation and adapters ([`runner/v1`](extension-points.md#runnerv1)) |
| `shibu exec <vm> -- <command>` / `shibu cat <vm>:<path>` | `exec` and `read_file` on `<vm>/mcp`, through the caller's grant ([`agents.md`, VM `mcp`](agents.md#vm-mcp-contract-mcpv1)) |
| `shibu exposure <vm \| runner \| secret>` | `exposure` ([`kernel-api.md`](kernel-api.md#exposure)) |
| `shibu agents [path] [--phase waiting-on-human]` | `list(path, selector=kind:agent[,phase:…], include_grants=true)`, with the status labels |
| `shibu chat <agent>` | `GET <agent>/messages?stream=true` and `POST <agent>/messages`; every type rendered as text ([`ui-conventions.md`](ui-conventions.md#messages-types)), questions answered by number, approvals by `approve` or `deny` |
| `shibu send <agent> <text>` | `POST <agent>/messages` |
| `shibu reply <agent> <message> [--choice N \| --approve \| --deny] [--reason …] [text]` | `POST <agent>/messages` with `reply_to` |
| `shibu artifacts <agent>` | `GET <agent>/artifacts` |
| `shibu stop <agent> [--reason …]` | `stop` |
| `shibu open <ns>/<pane>` | opens the pane origin's URL in a browser, as the viewer ([`ui-conventions.md`](ui-conventions.md#agent-served-panes)) |
| `shibu panes [path]` | `GET <ns>/ui/panes` for each visible agent, and the caller's `*/ui/*` handles |

### Namespaces, resources and grants

| Command | Calls |
| --- | --- |
| `shibu ns create <path> [--lease …] [--keep …] [--grant …] [--dry-run]` | `create_namespace`. Outside access is a `--grant` on `/sys/egress` (`--grant /sys/egress:tunnel:hosts=*.npmjs.org:443`) |
| `shibu delete <ref>` | `delete` |
| `shibu ls [path] [-l selector] [--depth N]` | `list` |
| `shibu get <ref>` | `get` |
| `shibu watch [path] [-l selector] [--types …] [--lease-warning 10m]` | `watch` |
| `shibu create <allocator> <target> --spec <yaml \| @file> [--lease …] [--wait 10m] [--dry-run]` | `create` |
| `shibu snapshot <volume> <target> --allocator <alloc>` / `shibu clone <snapshot> <target> --allocator <alloc>` | `create` with `spec: {kind: volume, from: <volume>, read_only: true}`, and with `from: <snapshot>` (shorthands) |
| `shibu move <ref> --to <ns> [--payer <alloc>]` | `move` |
| `shibu endpoint create <path> --vm <vm> --port <n> [--allocator @decl.yaml]` / `shibu endpoint delete <path>` | `create_endpoint`, `delete_endpoint` |
| `shibu grant <object> --to <ns> --ops a,b [--limit k=v …] [--subtree] [--regrant] [--reserved] [--lease …] [--as <name>] [-l k=v …] [--dry-run]` | `grant`. Without `--regrant` the grantee can use the grant but not pass it on. `--reserved` sets the whole cap aside |
| `shibu publish <endpoint> --as <name> [--audience anyone \| <people>] [--verifier <endpoint>] [--ops …] [--lease …]` | `grant(<endpoint>, to=/sys/ingress, labels={ingress.shibu.dev/audience: …})`: a shorthand for a grant to `/sys/ingress` ([`ingress/v1`](extension-points.md#ingressv1)) |
| `shibu revoke <grant> [--kill]` | `revoke` |
| `shibu grants [path] [--given \| --received] [--unused-since <time\|duration>]` | `list_grants` |
| `shibu lease <ref> <ttl \| none>` | `set_lease` |
| `shibu label <ref> k=v … [k-]` | `label` |
| `shibu usage <ref>` | `get`, showing its `usage` field |
| `shibu batch -f <file> [--dry-run]` | `batch` |
| `shibu apply -f <file> [--dry-run] [--prune]` | the reconciler, run in the CLI as the caller: a diff against `list`, then one `batch` ([`reconciler/v1`](extension-points.md#reconcilerv1)) |

### Secrets

| Command | Calls |
| --- | --- |
| `shibu secret put <ns/name> [--adapter <name> [--credential <id>]] [--from-file <path>]` | `put_secret`. With `--adapter`, prompts from the adapter's manifest, as the UI's form does, with input hidden; for an `oauth` credential it calls `POST <runner>/logins` on a runner it can launch on, which runs the vendor's flow and writes the result, and opens the vendor's page ([`agents.md`, Adapter manifest](agents.md#adapter-manifest)) |
| `shibu secret ls [path]` | `list(path, selector=kind:secret)`: metadata, never values |

There is no `shibu secret get` and no `retire`: rotating is
`shibu secret put` again. A secret is read only through a `read`
grant, by the namespace holding it, with the API's `read`; giving a
runner or a component a key is `shibu grant <secret> --to <ns> --ops read --lease …`.

### Audit and cost

| Command | Calls |
| --- | --- |
| `shibu audit [path] [--since …] [--types …]` | `audit` |
| `shibu cost [path] [--since …] [--by namespace \| payer \| label:<k>]` | `cost` |

### `/sys` (root only)

| Command | Calls |
| --- | --- |
| `shibu sys get [--version …]` | `GET /sys/reconciler/spec` |
| `shibu sys apply <spec> [--dry-run]` | `POST /sys/reconciler/apply` |
| `shibu sys history` | `GET /sys/reconciler/history` |
| `shibu sys rollback <version>` | `POST /sys/reconciler/rollback` |
| `shibu sys status` | `GET /sys/reconciler/status` |
| `shibu sys plugins <spec> [--dry-run]` | `sys_plugins`, the kernel's one `/sys` operation; `shibu sys apply` calls it when drivers or the secrets backend change ([`sys-spec.md`](sys-spec.md#operations)) |

### Tunnels

The shim is part of the same command, shipped in the base images
([`web-proxy.md`, The shim](web-proxy.md#the-shim)):

| Command | What it does |
| --- | --- |
| `shibu forward <endpoint> <local-port> [--connect]` | client side of a tunnel |
| `shibu serve <endpoint> --to <host:port> [--proxy-protocol]` | server side of a tunnel |

### Convention commands

These drive a convention endpoint a namespace may serve, not a kernel
operation. The kernel knows nothing of them.

A **request** is a call to a namespace's `requests` endpoint asking it
for a grant, for quota or for a secret; the answer is a grant
([`ui-conventions.md`, Requests](ui-conventions.md#requests)). Requests
are not a kernel concept
([design 2.6.5](../shibu.md#265-requests-stay-in-the-api),
[F14](../shibu.md#f14-inbox-requests-need-structure)). These commands
target the `requests` convention endpoint, so a namespace that serves
no `requests` endpoint simply has no requests: `shibu requests` on it
finds none, and there is nothing to approve.

| Command | Calls |
| --- | --- |
| `shibu requests <ns>` | `GET <ns>/requests?state=open` |
| `shibu approve <ns>/requests/<id> [--ops …] [--limit k=v] [--lease …]` | `grant(...)`, then `POST <ns>/requests/<id>/answer`: the kernel's `grant`, then the convention's answer |
| `shibu approve <ns>/requests/<id> --deny --reason …` | `POST …/answer` with `deny`; no kernel call |

## CLI parity

The table the `ui/v1` conformance suite is built from
([design, CLI parity](../shibu.md#cli-parity)). Each row is a scenario
the suite runs through a UI and through `shibu`, as the same principal,
requiring the same API calls in the audit log and the same resulting
state.

| In a UI | From the CLI |
| --- | --- |
| act as a namespace | `shibu --as factory …` |
| the list of agents | `shibu agents [path] [--phase waiting-on-human]` |
| a chat | `shibu chat <agent>` |
| answer a question or an approval | inside `shibu chat`, or `shibu reply <agent> <message> …` |
| answer a request (convention) | `shibu approve <request> [--deny --reason …]` ([Convention commands](#convention-commands)) |
| an agent's artifacts | `shibu artifacts <agent>` |
| a pane | `shibu open <ns>/<pane>` |
| a credential form | `shibu secret put <ns/name> --adapter <name>` |
| create an agent (the distribution's helper, not a kernel call) | `shibu agent create … [--runs-in …] [--tools …]` |
| runners and their capacity | `shibu runners` |
| who can reach a VM, runner or secret | `shibu exposure <object>` |
| stop an agent | `shibu stop <agent>` |
| grants, quota and cost | `shibu grants`, `shibu usage`, `shibu cost` |
| share a chat or a pane | `shibu grant <agent>/messages --to /users/bob --ops write` |
| publish an endpoint | `shibu publish <endpoint> --as <name> --audience …` |
| edit the `/sys` spec | `shibu sys apply <spec>` |

A UI feature with no row here must still have a CLI command; if it
can't, the UI is using something that isn't in the API, and fails the
contract.
