# Local scripts

Local scripts give an agent narrow, declared reach onto the grain node
host. Each script an operator declares becomes one MCP tool,
`local_<name>`, that runs the script on the host with the agent's
arguments and nothing else. It is ported from Silo v2's local executor
(bwsalmon/silo `old/localexec`, Silo `design.md` §12, "remote agent,
narrow local reach"). This document is the design note for the port and
the reference for the declaration.

> [!NOTE]
> The `local-scripts:<name>` capability is retired (tasks 2366 and 3077).
> A declaration is now served by the `host-script` capability server:
> each of its scripts becomes one
> `https://grain.dev/capabilities/host-script#host-script` `{script}`
> grant, and the daemon rewrites stored `local-scripts:<name>` grants to
> those at every start ([Moving to host-script](#moving-to-host-script)).
> The declaration format below is still what an operator writes. Where
> this note describes a `local_<name>` MCP tool, it records the original
> port; the script is now run as `grain host-script <name>`.

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
  provider keys, proxy settings and other variables are withheld.
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

## Moving to host-script

`local-scripts:<name>` has retired onto the `host-script` capability
server ([capabilities.md](capabilities.md#host-scripts-grain-capability-host-script),
[host-scripts.md](host-scripts.md)). Each script of a declaration becomes
one host-script script, and a grant of the declaration becomes one
`…/host-script#host-script` `{script}` grant per script. Those grants
are still refused to the runs local-scripts refused, though host-script
itself has no such rule. A scheduled run is minted none of them, and the
rest of its grants as usual. A CI job asking for one is refused, whatever
the event (`localscripts.MigratedFrom`).

At every start, before the capability servers start, the daemon
(`daemon.migrateLocalScripts`) does the following:

- **Reads the declarations.** It reads every declaration it can find,
  in the state repository's `local-scripts/` and from each
  `-local-scripts` flag, the way a run's snapshot reads them.
- **Adds the scripts to host-script.** Each script is added to the
  host-script server that runs in the daemon's own environment, which
  is also where local scripts run. A `run: host` server is passed over.
  If there is no such server, it is created as
  `https://grain.dev/capabilities/host-script`. Each script is named
  `<declaration>-<tool name without local_>`, so `ops`'s
  `tools/deploy.sh` becomes `ops-deploy_sh`. Its body ends
  `exec '<the script's real path>' "$@"`, so the file in the state
  repository is still what runs, under its own `#!`. Before that, the
  body unsets the ambient identity host-script would pass on and
  local-scripts never did (`SSH_AUTH_SOCK`, `KUBECONFIG`,
  `GOOGLE_APPLICATION_CREDENTIALS`, `DOCKER_HOST` and the rest), unless
  the declaration's `env` sets it. On every call it also refuses (exit
  126) a file that is now a symlink, not a regular executable, not
  owned by grain's user, or writable by group or others, or that sits
  under a directory (up to the declaration's folder) writable by group
  or others, as the per-run snapshot did. Its `cwd` is the
  declaration's `workdir`, its `timeout` the script's timeout rounded
  up to whole seconds, its `approve` flag is preserved, and its `max_output` is the declaration's
  `output_cap`. `max_calls` is unlimited. Each `env` secret becomes
  `$env:GRAIN_LOCAL_SCRIPTS_<SECRET>`, and the server's own `env` binds
  that variable to `$secret:<name>`.
- **Converts again at every start.** Each body it writes starts
  `# grain: converted from local-scripts:<declaration>`. At every start
  the daemon first removes every script with that first line, and every
  `GRAIN_LOCAL_SCRIPTS_*` binding no remaining script uses, then
  converts the declarations as they are now. A script since removed, a
  declaration since deleted, and a
  changed `workdir`, timeout, `approve` flag, output cap or `env` therefore stop running
  as they were. A host-script grant of a script that is gone is refused.
  A declaration's new scripts are added at the next start. Granting
  them is a separate step: a grant names one script. When nothing
  changed, the config row is not written.
- **Leaves the operator's own scripts alone.** A script without that
  first line is the operator's, and is kept as it is. If it has the
  name a declaration's script would take, the declaration is skipped
  and the daemon logs why, so a `local-scripts:<name>`
  grant never becomes a grant of a script the declaration did not
  declare.
- **Rewrites stored grants.** Every stored `local-scripts:<name>` is
  rewritten to its declaration's host-script grants (`Store.RewriteRetiredGrants`).
  This covers task, template and schedule grants, default and CI
  capabilities, and console sessions.
- **Upgrades an older export.** A grain/config export still naming
  `local-scripts:<name>` imports as the same grants
  (`model.SetRuntimeGrantExpansions`, read by `model.RetiredGrantsBecame`),
  and grants expanded from `local-scripts:<name>` remain withheld from
  scheduled runs and refused to CI jobs (`localscripts.MigratedFrom`).
  Anywhere else the old name was an alias for one release, and is not
  any longer.

The `pkg/capability/intree` fidelity test runs one declaration (including
`approve: true` and stdin) both ways. Both give the same stdout, stderr,
exit status, working directory, approval preview/hash and secret.

What host-script does differently:

- A script is reached in the sandbox as `grain host-script <name> [args...]`
  (or `grain host-script --stdin <name> [args...]` when forwarding stdin),
  not as an MCP tool on `grain mcpserver`.
- A secret is resolved when the capability server starts (as the
  `capability-servers` principal), rather than per run as the task's team.
- A changed script file on disk is picked up on its next call rather than
  refused until the next run; ownership, mode, regular-file, and directory
  permission checks still run before every call.

The `local-scripts:<name>` dynamic provider and `grain mcpserver`'s
`-local-scripts` / `-local-scripts-env-file` flags have been retired.

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

**Opt-in and trust.** A declaration's scripts are granted to work by
`…/host-script#host-script` `{script}` grants, one per script, in a
repository's default capabilities or on a template, as other
capabilities are (formerly one `local-scripts:<name>` capability per
declaration, which still expands to those grants). A task that does
not hold one is served no script. Like the other write-scoped
capabilities (`repo-write:*`, `package-publish`, `github-sandbox`), it is
never granted to an untrusted run. A task a schedule filed is refused it
when its grants are resolved, so it does not dispatch, and the
orchestrator checks again before it serves anything. Every CI job is
refused it too, whatever the event: a CI job has no MCP server, and its
steps are whatever the pushed branch says
(`pkg/capability/localscripts`).

**Serving the scripts.** At daemon startup, `migrateLocalScripts` converts
each declaration onto the deployment's `host-script` capability server as
scripts named `<decl>-<script>`. When a run is granted one, `host-script`
places its descriptor in the sandbox for `grain host-script [--stdin] <name>`,
forwards its data-plane port through the capability port relay, and binds
the script's secrets from the capability server's environment. When the
attempt ends, releasing the grant revokes its token, kills anything still
running, and collects the call audit onto the run record.

**Approval.** When minting a grant on a server method that accepts approval
callbacks, the orchestrator hands over the daemon's loopback approval URL
and a per-task `ghsa_` bearer token (`protocol.ApprovalCallback`), which
is revoked when the capability pass is released. For each `approve: true`
call, `host-script`'s `localexec.HTTPApprover` posts
`POST /api/tasks/{id}/local-script-approvals` to the daemon. It then
long-polls `.../{approval}/wait` until somebody answers or the script's
timeout passes; at that point it posts `.../expire` and the call is
refused. While the call waits, the task shows it with Approve and Deny:
in the task pane, in the chat for an interactive task, and in the inbox
under "Local scripts waiting for approval" (`GET
/api/local-script-approvals`). Each shows the script, every argument
quoted, and (when `--stdin` was passed) the stdin byte count, SHA-256 and
first 4 KiB preview. An answer (`.../answer`, `{"approve": true|false}`) is
authorized like any other write to the task, and a run's own principal
(including its `ghsa_` callback token) is refused, so a run can never
approve its own call. The answer is recorded on the task as a comment by
whoever gave it, and the waiting call receives it. Waiting approvals live
in the daemon's memory, because a call waits only as long as its run.

## Code

| File | What |
| --- | --- |
| `pkg/localexec/snapshot.go` | The declaration, `Resolve`, `Snapshot` and its refusals, `Verify`, `Check` |
| `pkg/localexec/exec.go` | `Executor`: argument parsing, running one script with no shell, the timeout and kill, the environment |
| `pkg/localexec/warn.go` | `Warnings`: the static argument-to-code checks |
| `pkg/localexec/approve.go` | `Request`, `Approver`, `ApproverFunc`, `Queue` |
| `pkg/localexec/http_approve.go` | `HTTPApprover`: posting, waiting on, and expiring per-call approvals over HTTP |
| `pkg/localexec/headtail.go` | The bounded output buffer |
| `pkg/capability/localscripts` | Converting `local-scripts:<name>` declarations and grants onto `host-script` (`ConvertToHostScripts`, `MigrateToHostScript`, `MigratedFrom`) |
| `pkg/capability/intree/hostscript.go` | `host-script` capability server: `--stdin`, `approve: true`, execution, and call audit |
| `pkg/ui/local_script_approvals.go` | The daemon's approval registry, scoped callback tokens, and routes |
| `ui/src/components/detail/LocalScriptApprovals.jsx` | Approve and Deny, in the task pane, the chat and the inbox |
| `ui/e2e/local-scripts-approval-walkthrough.spec.js` | The walkthrough against `grain demo` |
