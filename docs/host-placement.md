# Which host runs host work

Host work -- an agent CLI started on the bare machine, a `run: hostd`
capability server, a host script -- runs on a host. **Which host is
a property of what does the work, never of the task**:

- a **model connection** names the host its agent CLI runs on, and with it
  the CLI login an ambient credential is (`model.AgentBackend.Hostd`,
  Settings -> Models -> the connection -> **Run on host**);
- a **capability server** names the host its process runs on
  (`protocol.ServerConfig.Hostd`, with `run: hostd`, Settings ->
  Capabilities -> the server -> **Host**).

A task, a template, a schedule, a suite, a qualification item, a path
trigger, a repository's settings and a grainfile or `.grain/settings.yml`
declaration carry no host. Something that still sends one -- `hostd` on
`POST /api/tasks` or a template, `defaultHostd` on a repository's
settings, `PUT /api/tasks/{id}/hostd`, a `hostd:` key in a declaration, or
`hostd` in the New Task form's configuration -- is refused with
"The host is set on the model connection or capability server, not the task",
never accepted and ignored.

## A connection's host

`hostd` is a selector: a registered host's name (`ws-a`), or
`label=value[,label=value]` for any connected host carrying every label
(`pool=gpu`). Empty leaves the CLI where the framework's run location says
(container, host or detached, through the deployment's default host), as
every connection did before.

A run on a connection placed on a host:

- starts its agent CLI there, whatever the framework's run location
  (`hostdRoute.apply` makes it `host` for the run unless it is already
  `host` or `detached`);
- counts against that host's capacity (its allowlist's `capacity:`);
- keys a usage limit on an ambient credential (an Antigravity ambient
  login, a Codex subscription) to that host, so a limit hit on one
  machine does not pause the same connection on another;
- is recorded in `task_run_hostd` (host, instance, owner), shown on the
  run's timeline row as "ran on host ws-a on build-box · owner".

While the host is not connected the connection is **unavailable**: a run
pinned to the connection waits (`waiting for hostd ws-a`), and the rotation
passes the connection over (`N whose hostd cannot take a run now`). It never
falls back silently. `hostdFallback`, an ordered list of further selectors,
empty by default, is the only way a run on the connection goes elsewhere:
each is tried in order while the ones before it are away.

## A capability server's host

```json
{
  "mcpServers": {
    "https://example.com/caps/host-scripts-ws-a": {
      "command": "grain-host-scripts",
      "run": "hostd",
      "hostd": "ws-a"
    },
    "https://example.com/caps/host-scripts-gpu": {
      "command": "grain-host-scripts",
      "run": "hostd",
      "hostd": { "labels": { "pool": "gpu" } }
    }
  }
}
```

`run: hostd` is `run: host` under its new name; both are read alike, and
`run: host` with no `hostd` is the default host, as before. `hostd:` is a
name, a `k=v` selector string, or `{"name": ...}` / `{"labels": {...}}`.
Each entry is one instance on one host: the same server type on two hosts
is two entries under two capability ids.

A placed server is attached in its host's own registry, not the
deployment's; every run that holds a grant of it reaches it there, whatever
host the run's agent CLI went to. A run whose connection is on host A and
which holds a grant of a server on host B so starts its CLI on A and calls
the server on B. The run's `task_run_hostd` row says A; each of the
server's capability audit records (`task_run_capability_audit.hostd`) says
B. While a placed server's host is not connected, a grant of it cannot be
minted and the task waits with `needs-hostd:<selector>`.

## Migration

Schema 111 drops `task_hostd`, `template.hostd` and
`repo_config.default_hostd`. `task_run_hostd` is kept: it is the read-only
record of where each run's agent CLI ran, and the run history keeps
showing it. A deployment that pinned tasks or repositories to a host
moves the pin to the connection (or connections) those tasks ran on, in
Settings -> Models.
