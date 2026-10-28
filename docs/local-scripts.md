# Local scripts

Local scripts give an agent narrow, declared reach onto the grain node
host. Each script an operator declares becomes one MCP tool,
`local_<name>`, that runs the script on the host with the agent's
arguments and nothing else. It is ported from Silo v2's local executor
(bwsalmon/silo `old/localexec`, Silo `design.md` §12, "remote agent,
narrow local reach"). This document is the design note for the port and
the reference for the declaration.

## What carries over from Silo

Silo ran scripts on the invoking machine, outside the sandbox, because
that is where they were trusted. In grain that machine is the node host,
and every one of Silo's guarantees is kept:

- **Snapshot at start.** The scripts are listed, checked and hashed when
  the run starts. A script that has changed since then (content, size or
  mode) is refused on its next call; the next run serves the new version.
  A script is refused at snapshot, and the whole snapshot fails, if it
  resolves outside the declaration's folder, is not a regular executable
  file, is not owned by grain's own user, or it or any directory between
  it and the folder is writable by group or others.
- **No shell.** The tool takes `args` (a list of strings) and optional
  `stdin`, and the script is executed directly with exactly those
  arguments. No shell sees them, so `$(...)`, `;` and globs reach the
  script as plain text. A NUL byte in an argument is refused.
- **Timeout and output cap.** Each call is bounded by the script's
  timeout (default 5 minutes). When it passes, the script's process group
  gets SIGTERM and, 5 seconds later, SIGKILL. Stdout and stderr are each
  capped (default 1 MiB), keeping the first and last halves and saying
  how much was dropped. A non-zero exit is an ordinary result, reported
  as JSON with the exit code, signal, duration, byte counts and output.
- **Per-script credentials.** A script's `env` entries name secrets that
  are delivered to that script alone as environment variables. Every
  script otherwise gets only a minimal environment (`PATH`, `HOME`,
  `USER`, locale, `TZ`, `TMPDIR`, `TERM`, CA bundle paths): the daemon's
  provider keys, proxy settings and other variables are withheld, as they
  are from a [custom agent backend](custom-agent-backends.md#environment).
- **Approve before run.** A script marked `approve: true` waits for a
  person to approve each call, showing the script, its arguments, how
  many bytes of stdin it gets, the SHA-256 of that stdin, and its first
  4 KiB (marked as truncated when there is more), so a script that reads
  its instructions from stdin is approved by somebody who can see them. The wait is bounded by the script's
  timeout; an unanswered call is refused and the script never runs. The
  script is re-hashed after the approval, in case it changed while the
  call waited.
- **Static warnings.** `Declaration.Check` reports the snapshot's
  refusals as errors and, as warnings, the obvious ways a script turns an
  argument into code: `eval`, a shell's `-c` with a positional parameter,
  handing `"$@"` to an interpreter, unquoted `$@`/`$*`, `xargs`, and a
  "script" that is itself a shell or interpreter. They are best-effort and
  line by line; the operator wrote the script.

Left behind: Silo's terminal approver and desktop notifications (a grain
run has no terminal; the approval goes to grain's UI instead), and
`silo approve` over a control socket (replaced by the UI and the API
behind it).

## The declaration

A local-scripts declaration is a YAML file. Its folder is the directory
that holds it, and every script must lie inside that folder.

```yaml
scripts:
  dir: scripts            # every executable directly in it; default "scripts" if present
  list:
    - path: tools/deploy-preview
      description: Deploy a preview of a branch.
      approve: true       # each call waits for a person
      timeout: 2m
      env:
        DEPLOY_TOKEN: preview-deploy-token   # variable -> secret name
workdir: .                # where every script runs; default the folder
timeout: 5m               # default per-call timeout
output_cap: 1048576       # bytes, per stream
```

Unknown fields are refused. A script's tool is `local_` followed by its
file name, lowercased, with anything outside `[a-z0-9_]` turned into
`_`; two scripts that would be served as the same tool are refused. A
script describes itself with `# grain:` lines (joined into the tool's
description, unless `description` is set) and one `# grain-args:` usage
line, within its first 20 lines. Silo's `# silo:` and `# silo-args:`
lines are read too, so a script written for Silo carries over unchanged.

## Wiring

**Where scripts are declared.** Never in the repository the agent works
on: the agent pushes branches there, and a script it could edit would be
code it runs on the host. A declaration named `<name>` is read from one
of two places, and a host-managed one wins:

- the deployment's state repository (grain/config), which runs cannot
  write, at `local-scripts/<name>/local-scripts.yaml` with its scripts
  beside it. The node host reads its own checkout of it,
  `<data-dir>/state-repo`;
- an absolute path on a host-managed installation, named with the
  daemon's repeatable `-local-scripts <name>=/abs/path/local-scripts.yaml`
  flag.

The snapshot's ownership and permission checks apply to the copy on the
node host. A name is lowercase letters, digits, `-` and `_`.

**Opt-in and trust.** A declaration is granted to work by a
`local-scripts:<name>` capability, in a repository's default
capabilities or on a template, as other capabilities are. A task that
does not hold it is served no local tool. Like the other write-scoped
capabilities (`repo-write:*`, `package-publish`, `github-sandbox`), it is
never granted to an untrusted run. A task a schedule filed is refused it
when its grants are resolved, so it does not dispatch, and the
orchestrator checks again before it serves anything. Every CI job is
refused it too, whatever the event: a CI job has no MCP server, and its
steps are whatever the pushed branch says
(`pkg/capability/localscripts`).

**Serving the tools.** The run's forked `grain mcpserver` runs on the
node host, beside the agent CLI. Once the daemon has checked the
capability, it passes that process `-local-scripts <name>=<path>` for
each declaration. It also passes `-local-scripts-env-file`, a 0600 JSON
file of the secrets the scripts' `env` entries name, resolved from the
secret store as the task's team and removed when the run ends. That is
the same pattern as `RunConfig.SandboxEnvFile`: only the path is in
argv. Both travel with the run's grants (`agent.GrantArgs`), so claude,
codex, antigravity and custom backends all pass them. The server checks
again that the task holds each capability, snapshots the scripts when it
starts, and registers each executor's `Tools()` beside its other tools.
When the run ends it closes them, killing anything still running. A
declaration that fails its snapshot is reported on the server's stderr
and nothing from it is served. Each call is recorded in the run's
tool-call record like any other.

**Approval.** For each `approve: true` call, the server's Approver posts
`POST /api/tasks/{id}/local-script-approvals` to the daemon. It then
long-polls `.../{approval}/wait` until somebody answers or the script's
timeout passes; at that point it posts `.../expire` and the call is
refused. While the call waits, the task shows it with Approve and Deny:
in the task pane, in the chat for an interactive task, and in the inbox
under "Local scripts waiting for approval" (`GET
/api/local-script-approvals`). Each shows the script and every argument,
quoted. An answer (`.../answer`, `{"approve": true|false}`) is
authorized like any other write to the task, and a run's own principal
is refused, so a run can never approve its own call. The answer is
recorded on the task as a comment by whoever gave it, and the waiting
call receives it. Waiting approvals live in the daemon's memory, because
a call waits only as long as its run.

## Code

| File | What |
| --- | --- |
| `pkg/localexec/snapshot.go` | The declaration, `Resolve`, `Snapshot` and its refusals, `Verify`, `Check` |
| `pkg/localexec/exec.go` | `Executor`: the tools, argument parsing, running one script with no shell, the timeout and kill, the environment |
| `pkg/localexec/warn.go` | `Warnings`: the static argument-to-code checks |
| `pkg/localexec/approve.go` | `Approver`, `ApproverFunc`, `Queue` |
| `pkg/localexec/headtail.go` | The bounded output buffer |
| `pkg/capability/localscripts` | The `local-scripts:<name>` capability: where declarations are found, the untrusted-run refusals, the credentials file, `Open` |
| `pkg/daemon/localscripts_mcp.go` | The mcpserver's `-local-scripts` flags, serving the tools, and the Approver that asks the daemon |
| `pkg/ui/local_script_approvals.go` | The daemon's approval registry and its routes |
| `ui/src/components/detail/LocalScriptApprovals.jsx` | Approve and Deny, in the task pane, the chat and the inbox |
| `ui/e2e/local-scripts-approval-walkthrough.spec.js` | The walkthrough against `grain demo` |
