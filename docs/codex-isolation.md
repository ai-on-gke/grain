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
Retries reuse the same endpoint and home for that run. When Codex runs
`detached` in `grain host` (`-codex-run=detached`, `cli codex-detached`),
the host wrapper (`grain codex-detached`) starts `mcp.StartCommandHTTP`
outside the daemon from `$CODEX_HOME/mcpserver.json`, writes the loopback
URL, bearer token and tools snapshot into `$CODEX_HOME/config.toml`, and
serves the endpoint for the lifetime of the detached `codex.bin` process so
that a daemon restart does not interrupt the run's MCP tools. Claude and
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
environment configuration. Without an environment, an environment-backed
tool that is still registered refuses a call at dispatch ("No usable
execution environment is available"). It does not stop the tool from
being advertised or registered. Read-only mode alone did **not** protect
controller reads.

Up to 0.153.0, codex registered shell/exec, stdin, apply-patch and local
image reading only when the turn had an environment. 0.166.0 removed that
gate from `core/src/tools/spec_plan.rs`. Those tools now depend only on
features and model metadata. Grain therefore removes each one by its own
switch, and the manifest above stays as a second layer:

```toml
# config.toml
[features]
shell_tool = false      # exec_command / shell (and write_stdin with it)
unified_exec = false    # exec_command + write_stdin
view_image = false
```

`request_permissions` is behind `request_permissions_tool`, which is
under development and off by default.

`apply_patch` has no feature switch. Codex registers it whenever the
model's catalog entry has an `apply_patch_tool_type`, as `gpt-5.5` and the
Code Mode models do. The catalog entry is the only input, and
`model_catalog_json` is the only configuration that replaces it. Before a
run starts, Grain runs the installed binary's `codex debug models` in the
run's private home. That is the bundled catalog, refreshed against the
run's own provider and login the way the run itself would refresh it.
Grain sets `apply_patch_tool_type` to `null` on every model and leaves
every other field as codex wrote it. It writes the result to
`$CODEX_HOME/model-catalog.json` and names that file in `model_catalog_json`.
A model that is not in the catalog gets codex's fallback metadata, which
has no `apply_patch_tool_type` either. The run fails if the dump fails or
lists no models. Like the rest of the run's files, the catalog is removed
when the run ends. Interactive terminals get the same catalog. The one
side effect we observed: with a configured catalog, codex skips its own
trimming of update-plan text from the base instructions.

The policy therefore depends on behaviour that is specific to this
version of codex. That is why there is a version gate and regression
probes.

Codex starts in its private home, not the controller workspace or sandbox
checkout. Project configuration from those directories cannot supply tools
or instructions. Environment-context injection, apps, plugins, multi-agent
features and local skills are disabled. `approval_policy="never"` and
`sandbox_mode="read-only"` remain defense in depth.

`features.code_mode_host=true` allows models such as Sol to orchestrate
MCP calls through the JS host. It does not restore a local execution
environment. The nested tools Code Mode exposes are the registered ones,
so the switches above also keep native tools out of the JS host's
`tools` object. On 0.166.0 without them, the probe's Code Mode script
found native tools there, threw `NATIVE_TOOL_EXPOSED`, and never reached
the MCP write. The image installs the matching `codex-code-mode-host` release.
Codex resolves the host beside its real executable, so the image must
provide `/opt/codex/codex-code-mode-host`; a symlink on `PATH` alone is
insufficient. The image build and container tests check that this sibling
executable launches.
Nested MCP calls continue to be recorded and parsed as Grain tool calls.

## Future features and compatibility

Stock Codex does not expose a universal default-deny dispatcher allowlist.
This is an audited configuration for **codex-cli 0.167.0**, not an OS-level
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
profiles and with Sol and Astra Code Mode. All of these come from the
binary's own catalog (0.166.0 bundles Astra), with apply-patch stripped as
described above. The tests do not assert an account's access to any of
these models and do not run a live model.

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
rejected by the dispatcher as unsupported (not registered, rather than
registered and refused for want of an environment), a native patch cannot
create a controller canary, and nested MCP calls appear in Grain's parsed tool record. No live
model or real API credential is used. The nightly credential-free probe
step includes this test and installs the matching Code Mode host.

Unit tests cover HTTP authentication/origin/host restrictions, startup and
cancellation, configuration/effort persistence, model catalog pagination,
unknown CLI rejection, and UI selection/fallback behavior.

Official interfaces: [MCP configuration](https://developers.openai.com/codex/mcp)
and [app-server model discovery](https://developers.openai.com/codex/app-server).
The tool gating was checked against the pinned upstream
`core/src/tools/spec_plan.rs` (`add_shell_tools`, `add_core_utility_tools`),
`features/src/lib.rs`, `models-manager/src/model_info.rs` and
`core/src/config/mod.rs` (`model_catalog_json`). The no-environment
behavior was checked against `exec-server/src/environment_provider.rs` and
`environment_toml.rs`. All of this is specific to the implementation, which
is why there is a version gate and regression probes.
