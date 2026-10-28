# Custom agent backends

A custom agent backend lets a deployment use an agent other than Antigravity,
Claude, or Codex without rebuilding Grain. It is an executable committed to
the deployment's state repository. Grain starts it once per run and exchanges
one JSON request and response.

This document is the complete public interface. Implementing a backend does
not require reading Grain's source.

## Configure a backend

Commit an executable to the state repository, for example
`agent-backends/my-agent`. In **Team settings → Connections**, choose
**New connection**, pick **Custom command**, and enter that path in
**Command**. Relative paths are resolved from the root of the checked-out
state repository. Absolute paths are also accepted for host-managed
installations. A custom command cannot be asked which models it serves, so
list them under **Advertised models** on the same page. The reasoning effort
for each model is set on the team's **Models** tab.

Model and effort are opaque values: Grain does not interpret
them. It passes them to the command exactly as configured. A custom backend
may ignore any it does not use. Grain routes model requests through its internal
model proxy and provides a scoped `model_proxy_url` and `model_proxy_token`
rather than exposing raw model credentials. The token is minted for one run
and revoked as soon as the command exits; it is good for nothing afterwards.
Grain picks the proxy from the backend's model (or, failing that, its name):
`claude`, `anthropic`, `sonnet`, `opus` and `haiku` select Claude; `openai`,
`gpt`, `codex` and a leading `o1`, `o3` or `o4` select OpenAI; `gemini`,
`google` and `flash` select Gemini. A backend
whose vendor cannot be told that way, or whose vendor's proxy is not running,
is dispatched with empty model proxy fields rather than being refused a run —
it must then authenticate some other way, or have nothing to authenticate to.

A credential entered for a custom backend in **Team settings → Connections** is not
delivered to its command. The model proxy token is the only model credential
a custom backend receives. Requests on it are forwarded with the key of the
backend's team's own connection for that vendor (a Claude, Codex or
Antigravity connection of the same team), which stays with the proxy; a team
with no connection for the vendor has every request refused, and no
deployment-wide credential stands in.

A custom backend is a connection like any other
([connections and models](connections-and-models.md)). It advertises the
models it can serve, and each run hands it the model that run chose and the
team's effort for that model. A task that chooses a model a custom
connection advertises is round-robined onto it along with every other
enabled connection of the team that serves that model. A task can also pin
one connection outright, custom connections included. It is then dispatched
onto that connection rather than taking its turn in the rotation, including
a connection disabled in **Team settings → Connections**, which is otherwise
skipped.

## Process contract

Grain executes the configured file with no arguments. The working directory is
the run's sandbox when it is local. Grain writes one UTF-8 JSON object to
standard input, closes stdin, and waits. The command must write exactly one
UTF-8 JSON object to standard output and exit. Diagnostic output belongs on
standard error.

### Environment

The command does not inherit the daemon's environment. It gets only:

- `HOME`: an empty directory made for this run alone and deleted when the
  command exits. It is neither the daemon's home directory nor the sandbox.
  A backend may keep caches and configuration there, but nothing carries over
  into the next run.
- `PATH`: the daemon's own, or
  `/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin` when the
  daemon has none.
- `USER`, `LOGNAME`, `SHELL`, `LANG`, `LANGUAGE`, `LC_*`, `TZ`, `TMPDIR`,
  `TERM`, `XDG_RUNTIME_DIR`, `SSL_CERT_FILE` and `SSL_CERT_DIR`, where the
  daemon has them set.

Provider API keys, cloud credentials, proxy settings (`HTTP_PROXY` and
friends, which can embed credentials) and the daemon's other variables are
withheld. A backend that needs other settings must get them itself, for
example from a file committed beside it or an absolute path on a host-managed
installation.

The current protocol version is `2`. A backend should reject versions it
does not implement. Field names and enum values are stable within a version;
implementations should ignore unknown fields so additions remain compatible.

The request is:

```json
{
  "protocol_version": 2,
  "operation": "run",
  "prompt": "the complete task prompt",
  "working_directory": "/absolute/path/to/the/sandbox",
  "model": "deployment-defined model name",
  "effort": "deployment-defined effort",
  "model_proxy_url": "http://127.0.0.1:41234",
  "model_proxy_token": "token-value",
  "max_turns": 50,
  "deadline": "2026-09-08T20:15:00Z",
  "mcp_server": {
    "command": "/absolute/path/to/grain",
    "args": ["mcpserver", "-sandbox-root", "/absolute/path/to/the/sandbox"],
    "env": []
  }
}
```

Only `protocol_version`, `operation`, `prompt`, and `mcp_server.command`
are always present. In particular, `working_directory` is absent for a
remote sandbox. Use the supplied MCP server in both cases; its arguments
already contain the correct transport and authorization. `deadline` is
RFC 3339 UTC. The command is also cancelled when the run context ends.

`mcp_server` describes a standard MCP server over stdio. Start that command
with the listed arguments and environment additions, then configure the agent
or SDK as an MCP client. Do not modify the arguments. They scope filesystem,
Git, CI, task, and optional capability tools to this run. Environment entries
are `NAME=value` additions to the environment the command itself was given,
replacing any variable of the same name. They carry what the server needs
and the command's own environment lacks, such as the daemon's `HOME` and the
Docker client's `DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CONFIG`,
`DOCKER_CERT_PATH` and `DOCKER_TLS_VERIFY`. The server uses those to reach a
container sandbox. Pass them to the server, and do not rely on them in the
backend itself.

`mcp_servers`, present only for a run whose capability grants expose MCP
servers ([`docs/capabilities-spec.md`](capabilities-spec.md) §5.2.4), lists
them in grant order, each as `{"name": ..., "type": "http", "url": ...,
"tools": [...]}`. Connect to each `url` as a Streamable HTTP MCP server beside
`mcp_server`, under its `name`, and admit the `tools` it lists. The URL is a
loopback proxy on grain's own host, and its path is its credential: do not log
or persist it. A mapping's `claude-json` and `codex-toml` configs carry the
same servers.

On success, return:

```json
{
  "protocol_version": 2,
  "result": {
    "final_text": "final answer shown for the run",
    "tool_calls": [
      {
        "name": "comment_on_issue",
        "arguments": {"body": "implementation notes"},
        "text": "tool result text",
        "is_error": false
      }
    ],
    "transcript": "human-readable chronological transcript"
  }
}
```

The response field names are part of protocol version 2 and must be written exactly as shown.
`tool_calls` must include every MCP tool call in chronological order and use
the bare tool name (for example `ask_question`, not a server-prefixed name).
Grain uses these records to relay questions, task proposals, review comments,
and closing notes. `transcript` may be empty, but a readable transcript is
strongly recommended for diagnosing runs.

On failure, return an error, optionally alongside a partial result:

```json
{
  "protocol_version": 2,
  "result": {
    "final_text": "",
    "tool_calls": [],
    "transcript": "work completed before the error"
  },
  "error": {
    "message": "provider refused the request",
    "kind": "usage_limit",
    "resume_at": "2026-09-09T00:00:00Z"
  }
}
```

`message` is required. `kind` is normally omitted. Set it to
`usage_limit` only when the account quota is exhausted; Grain will pause and
rotate that backend. Such an error may specify either `resume_at` (RFC 3339)
or `retry_after` (a positive Go duration such as `30m`), not both; if both
are sent, `resume_at` wins. A value Grain cannot read is ignored -- the
backend is still paused, for Grain's default window, and the run's error
message names the value it could not read. A usage-limit pause covers only
the model the run was on, unless the error says otherwise: send
`"scope": "account"` when the whole key is refused (credits, billing, an
organisation quota), and `"model"` to name the concrete model refused when it
is not the one Grain passed (see [Usage limits](connections-and-models.md#usage-limits)). With no
`scope`, Grain reads the message for the same hints it reads every framework's
error for. Other errors fail the run normally.

Exit zero after writing a valid success or error response. A non-zero exit is
also treated as a failure; Grain will still preserve a valid partial result.
Invalid JSON, a mismatched protocol version, or a response containing neither
`result` nor `error` is a backend failure.

## Declared CLI mappings

A custom backend can also be an existing agent CLI with no wrapper at all.
Point **Command** at a YAML file (a path ending in `.yaml` or `.yml`) in
the state repository instead of an executable. The file declares how each
of a run's settings reaches the CLI, and Grain refuses to start the
backend when a setting the run has is not declared. The format is the
same as a Silo v2 Silofile's `agent.custom` block (Silo's
`cli-mapping.md` §2.4), with `binary` beside it:

```yaml
binary: agent-backends/bin/my-agent   # absolute, or relative to the state repository
argv:
  headless: ["--prompt-file", "{prompt}", "--jsonl"]
mcp_config:
  format: claude-json                 # claude-json | codex-toml
  arg: ["--mcp", "{file}"]            # and/or env: MY_AGENT_MCP_CONFIG
permissions:
  none: ["--no-builtin-tools"]        # [] if the CLI has no built-in tools
events: claude-stream-json            # or: none
model: ["--model", "{model}"]         # required when the backend has a model
effort: ["--effort", "{effort}"]      # required when the backend has an effort
max_turns: ["--max-turns", "{max_turns}"]   # optional
model_proxy:                          # optional
  url_env: ANTHROPIC_BASE_URL
  token_env: ANTHROPIC_API_KEY
```

The command line is `binary`, then `argv.headless`, `mcp_config.arg`,
`model`, `effort`, `max_turns` (each only when the run has that setting)
and `permissions.none`, with these placeholders substituted:

- `{prompt}`: a file holding the run's prompt. `argv.headless` must name
  it.
- `{file}`: the MCP config file. `claude-json` is
  `{"mcpServers": {"grain-sandbox": {"command": ..., "args": [...], "env": {...}}}}`;
  `codex-toml` is the same server as a `[mcp_servers."grain-sandbox"]`
  table with `required = true`. Both describe Grain's stdio MCP server
  exactly as `mcp_server` does above. `mcp_config.env`, when set, names
  the variable that carries the file's path; the config must reach the
  CLI one way or the other.
- `{model}`, `{effort}`, `{max_turns}`: the backend's model and effort and
  the run's turn limit.

Both files are staged in a directory under the run's private `HOME`, not
in the sandbox. The CLI runs in the sandbox directory when the sandbox is
local, and in that `HOME` otherwise, with the environment described under
[Environment](#environment) plus `mcp_config.env` and the two
`model_proxy` variables. The proxy URL and token are delivered only when
`model_proxy` is declared.

`events` says how standard output is read. `claude-stream-json` reads
Claude Code's `--output-format stream-json` events, which gives Grain the
run's tool calls (and so its questions, proposals and closing notes), its
final text and a transcript. `none` takes the whole of standard output as
the final text and records no tool calls, so tools whose effect Grain reads
from the call record (`ask_question`, `comment_on_issue`, `propose_task`)
are not relayed; use it only for a CLI with no structured output. A
non-zero exit fails the run, keeping whatever was read.

Grain refuses, when the backend is built (so before any run starts):

- an unknown field, or a missing `binary`, `mcp_config.format`,
  `permissions.none` or `events`;
- `argv.headless` that never names `{prompt}`, or an MCP config that is
  neither passed with `{file}` nor named by `mcp_config.env`;
- a model or effort configured for the backend with no `model` or
  `effort` argument to carry it;
- `argv.interactive`: custom backends run unattended only;
- `permissions.all`: the CLI runs on the node host, and reaches the
  sandbox only through Grain's MCP server, so every run switches the CLI's
  built-in tools off;
- `mcp_config.format: stdio-bridge`, which Silo reserved and Grain does
  not need, since its MCP server is already stdio.

## Minimal Python backend

This example demonstrates the wire protocol; replace `run_agent` with the
agent SDK or CLI being adapted.

```python
#!/usr/bin/env python3
import json
import os
import subprocess
import sys

request = json.load(sys.stdin)
if request["protocol_version"] != 2 or request["operation"] != "run":
    raise SystemExit("unsupported custom backend request")

mcp = request["mcp_server"]
server = subprocess.Popen(
    [mcp["command"], *mcp.get("args", [])],
    env={**os.environ, **dict(e.split("=", 1) for e in mcp.get("env", []))},
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
)
# Configure your agent as an MCP client connected to server.stdin/stdout,
# give it request["prompt"], and collect its final text and tool calls.
final_text = run_agent(request, server)

json.dump({
    "protocol_version": 2,
    "result": {
        "final_text": final_text,
        "tool_calls": [],
        "transcript": final_text,
    },
}, sys.stdout)
```

Make the file executable (`chmod +x agent-backends/my-agent`) and test it
outside Grain by piping a captured or hand-written version-2 request into it.
Keep credentials out of fixtures and logs.

Custom commands currently support unattended runs only. They do not implement
Grain's optional interactive-terminal interface, so an interactive task that
selects `custom` is rejected before the command starts.
