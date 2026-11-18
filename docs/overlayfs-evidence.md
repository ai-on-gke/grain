# Collect OverlayFS restart evidence through a host script

The task-79 collector captures the fixed incident window
2026-11-18T03:10:50Z–03:11:10Z and a current mount-namespace snapshot.
It makes no mount, container, service or configuration changes.

The repository contains two executables:

- `scripts/overlayfs-evidence.py` collects evidence as JSONL on stdout.
- `scripts/overlayfs-evidence-host-script.py` generates a capability-server
  registration bundle containing the collector's complete source and its
  task grant. The generator reads repository files and prints JSON; it
  does not install anything or contact Grain.

Generate the reviewable bundle from the checked-out revision:

```sh
python3 -I scripts/overlayfs-evidence-host-script.py > overlayfs-evidence-registration.json
```

The registration embeds the collector in the operator-controlled host
script. Execution therefore needs neither a host checkout of this branch
nor an upgrade/restart of the daemon image. The host needs
`/usr/bin/python3`, Docker CLI, systemctl and journalctl. Missing tools
and failed reads are recorded as unavailable evidence.

## Register separately from this code PR

This deployment's settings live in **grain/config**, not this code
repository. Prepare the configuration change there, preserving the
current exported row and all unrelated settings:

1. Read the latest `tables/grain_config.json`. In its `mcp_servers`
   JSON value, locate `https://grain.dev/capabilities/host-script`.
   At preparation time this built-in has no configured scripts or host
   run location. Recheck before applying: if it now serves other scripts,
   assess their run-location requirements before moving the server.
2. Merge the generated bundle's `entry` into that server, retaining
   unrelated fields and scripts. Use `builtin: host-script`,
   `run: host`, `command: grain-host-script`, and no command arguments.
   Add the generated `overlayfs-evidence` script by name; do not replace
   the whole server map or reset the rest of `grain_config`.
3. Open the separate grain/config PR and validate with `grain state check`
   as that repository prescribes. A merged config reloads without a
   daemon restart. The host agent must offer its standard
   `grain-host-script` allowlist entry. Do not substitute a container
   run when host execution is unavailable.
4. In **Settings → Capabilities → Capability servers → host-script**,
   confirm the server is healthy and runs on the deployment host.
   The generated script has `approve: true`: preserve it.

The same server fields and script are visible/editable through the
capability-server settings API for deployments managed that way; this
task has no capability-configuration administration grant. Do not bypass
the deployment's configuration PR policy.

The bundle also provides the exact task grant:

```json
{
  "capability": "https://grain.dev/capabilities/host-script#host-script",
  "with": {"script": "overlayfs-evidence"}
}
```

Add only this grant to task 79 (**Task → Grants → Add grant → host-script**),
then redispatch it so the new attempt receives the descriptor and proxy.
Changing server settings alone does not grant a task access.

## Call and approve

Once granted, in the task sandbox:

```sh
grain host-script --list
grain host-script overlayfs-evidence --grain-help
grain host-script overlayfs-evidence > /tmp/overlayfs-evidence.jsonl
```

The final command waits for **Approve** in the task's approval queue
(task pane or inbox). Denial/refusal exits 125 and does not run the
collector. No caller arguments or stdin are accepted. Per-call timeout
is 600 seconds including approval; the collector itself has a 45-second
budget and each command has at most four seconds. Each grant allows
three calls.

The host-script transport allows 3 MiB per stream; the collector caps
JSONL output at 2 MiB, reserving a final coverage summary. Download or
attach the resulting file rather than printing it all into a tool
response. Use the `capture` record's hostname/UID and the configured host
agent identity to confirm the expected host was reached.

## Evidence and coverage

The collector captures:

- Kernel/runtime versions and local Docker storage configuration.
- Installed grain-daemon unit, effective startup/stop properties, and
  the standard `/usr/local/lib/grain/extract-host-binaries.sh` contents.
- Bounded boot, kernel, Docker/containerd and daemon journal evidence.
- Docker's retained container events and minimal inspect metadata for
  retained event IDs plus the named daemon, without container env.
- Overlay mountinfo per visible mount namespace, including filesystem
  device numbers and upper/work paths. Cloned namespace views of one
  filesystem device are distinguished from different devices claiming
  the same directory path.
- Root/cwd links and fdinfo mount IDs/flags for processes in those
  candidate overlapping namespaces. It never reads fd target contents,
  process environment or command lines.

Docker always uses the host's local Unix socket, ignoring an ambient
remote Docker context. Commands are fixed argv arrays, without shell
interpolation. The journal window and installed extractor path are
fixed in reviewed source. A nonstandard installed extractor path shown
by ExecStartPre needs separate assessment; it is not executed or
automatically followed.

Root-only journal reads use `sudo -n`. This never prompts and does not
install or broaden sudo policy. Host-script call approval does not grant
OS root privileges. Proc permission errors, missing sudo rights,
unavailable tools, vanished PIDs and exhausted limits are explicit;
a collector exit code of zero means a report was produced, not that
every section was accessible. Read its final summary and section status.

Limits are 250 journal entries per window, 64 KiB per command,
1,024 proc entries, 64 fdinfo entries per examined process, 32 processes
per candidate namespace, and 16 historical container IDs. Truncation
makes coverage incomplete. Reads race normal host activity.

Environment lines and common credential forms in text output are
redacted, but this is not a general secret detector. The approved report
can contain host paths, identities, installed startup source and bounded
journal text; keep it within the task's authorized readers.

This snapshot can identify current overlap candidates, not prove
concurrent writes or retrospectively classify the historical event.
Directory path aliases/inode identity, detached mount references and
removed containers may need additional evidence. Empty historical
journals/events do not prove retention, and clean current mounts do not
prove the restart was safe. If privileged sections are unavailable,
report that explicitly before proposing a narrower host privilege change.

## Verification

`go test ./tests/deploy -run '^TestOverlayFSEvidenceCollectorFixtures$'`
runs the Python fixtures: bounds/timeouts, permission failures, cloned
versus independent mount devices, fd references, redaction and refused
arguments. These fixtures do not contact Docker or systemd.

`go test ./pkg/capability/intree -run '^TestOverlayFSEvidenceCanBeCalledThroughHostdWithApproval$'`
uses a real local hostd and capability protocol with the generated script
body. It checks denied calls, argument rejection, approved execution as
the host agent's UID and parseable capture/summary records through the
sandbox CLI. It performs only the collector's fixed read-only operations
on the disposable test host. It does not verify live deployment
registration or live OS permissions; those require the separate config
change and task grant above.
