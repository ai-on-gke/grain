# Codex: MCP-only sandbox access

Grain runs the Codex CLI on the controller so its API key or shared
ChatGPT login never enters an agent-controlled sandbox. The model's file
and command operations must go through the run's Grain MCP tools.

## Runtime and lifecycle

Grain starts `grain mcpserver` with the same fixed sandbox, task and grant
arguments used previously. A Grain-owned adapter serves that child over
stateless Streamable HTTP on `127.0.0.1`, on a random port. Each run gets a
256-bit random bearer token. Unexpected Host headers, browser Origins,
missing tokens and another run's token are rejected. This is not a
network-accessible shared MCP service.

Grain snapshots the child's advertised tools at startup. Only those tools
are enabled and preapproved in that run's Codex configuration. The child
still owns the actual tool implementation and sandbox/grant binding. The
HTTP adapter does not accept arbitrary commands or sandbox targets.

Startup is bounded. Finishing a run, failing setup or closing a terminal
cleans up the endpoint, child process group and private Codex home.
Retries reuse the same endpoint and home for that run. Claude and
Antigravity keep their existing CLI-launched stdio transport.

## Native tool restrictions

Every run and interactive terminal receives a private `CODEX_HOME` with:

```toml
# environments.toml
default = "none"
include_local = false
```

There are no configured remote environments either. Grain also sets
`CODEX_EXEC_SERVER_URL=none`; the manifest takes precedence over inherited
environment configuration. The reviewed CLI removes environment-backed
tools from both the advertised surface and the executable registry:
shell/exec, stdin, apply-patch, local image reading and permission requests.
Read-only mode alone did **not** protect controller reads.

Codex starts in its private home, not the controller workspace or sandbox
checkout. Project configuration from those directories cannot supply tools
or instructions. Environment-context injection, apps, plugins, multi-agent
features and local skills are disabled. `approval_policy="never"` and
`sandbox_mode="read-only"` remain defense in depth.

`features.code_mode_host=true` allows models such as Sol to orchestrate
MCP calls through the JS host. It does not restore a local execution
environment. The image installs the matching `codex-code-mode-host` release.
Codex resolves the host beside its real executable, so the image must
provide `/opt/codex/codex-code-mode-host`; a symlink on `PATH` alone is
insufficient. The image build and container tests check that this sibling
executable launches.
Nested MCP calls continue to be recorded and parsed as Grain tool calls.

## Future features and compatibility

Stock Codex does not expose a universal default-deny dispatcher allowlist.
This is an audited configuration for **codex-cli 0.153.0**, not an OS-level
isolation boundary around the trusted CLI itself. Grain refuses both older
and newer CLI versions before starting an agent. An older binary could
ignore the configuration; a newer one could add non-environment-backed
tools. There is deliberately no automatic "accept newer" fallback.

A CLI update must change the Dockerfile and `ReviewedCLIVersion` together,
review environment-provider and tool-registry changes, and pass the
credential-free hostile-tool tests. A new Grain MCP tool is available only
when Grain's run-scoped registry grants/advertises it; it is not a native
host tool. A future runtime/tool architecture change requires another review.

This is a CLI-version requirement, **not a Sol-and-newer model minimum**.
The real pinned binary is tested with GPT-5.1-Codex and GPT-5.5 direct-tool
profiles, Sol Code Mode, and a synthetic Astra Code Mode metadata fixture.
The fixture is needed because the pinned bundled catalog predates Astra;
it does not assert an account's access to Astra or test a live model.

## Model and reasoning-effort controls

A Codex connection's page (**Team settings → Connections → Edit**) asks the
installed CLI's `app-server` for its paginated `model/list` and shows the
model IDs it returns, read-only, with a **Refresh**. What the connection
advertises is its own `models` list, which can take any of those IDs or a
custom model ID typed in when discovery fails. A connection does not choose
a model or an effort ([connections and models](connections-and-models.md)).
The team's **Models** tab chooses the default model and sets each model's
reasoning effort (`modelSettings`), from the framework's effort vocabulary.
The effort applies at the next dispatch and terminal launch, without a
restart.

## Verification

`TestLiveCodexMCPOnlyAcrossModelProfiles` uses mock Responses API messages
with the real Codex binary and a real Grain MCP child. It verifies a file
write through MCP reaches the sandbox, unadvertised native calls are
rejected by the dispatcher, a native patch cannot create a controller
canary, and nested MCP calls appear in Grain's parsed tool record. No live
model or real API credential is used. The nightly credential-free probe
step includes this test and installs the matching Code Mode host.

Unit tests cover HTTP authentication/origin/host restrictions, startup and
cancellation, configuration/effort persistence, model catalog pagination,
unknown CLI rejection, and UI selection/fallback behavior.

Official interfaces: [MCP configuration](https://developers.openai.com/codex/mcp)
and [app-server model discovery](https://developers.openai.com/codex/app-server).
The no-environment behavior was checked against the pinned upstream
`exec-server/src/environment_provider.rs`, `environment_toml.rs` and
`core/src/tools/spec_plan_tests.rs`; it is an implementation-dependent
part of this policy, hence the version gate and regression probes.
