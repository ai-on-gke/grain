# Which grain hostd host work runs on

Host work -- an agent CLI started on the bare machine, a `run: hostd`
capability server, a host script -- runs on a grain hostd. **Which hostd is
a property of what does the work, never of the task**:

- a **model connection** names the hostd its agent CLI runs on, and with it
  the CLI login an ambient credential is (`model.AgentBackend.Hostd`,
  Settings -> Models -> the connection -> **Run on**);
- a **capability server** names the hostd its process runs on
  (`protocol.ServerConfig.Hostd`, with `run: hostd`, Settings ->
  Capabilities -> the server -> **Hostd**).

A task, a template, a schedule, a suite, a qualification item, a path
trigger, a repository's settings and a grainfile or `.grain/settings.yml`
declaration carry no hostd. Something that still sends one -- `hostd` on
`POST /api/tasks` or a template, `defaultHostd` on a repository's
settings, `PUT /api/tasks/{id}/hostd`, a `hostd:` key in a declaration, or
`hostd` in the New Task form's configuration -- is refused with
"hostd is set on the model connection or capability server, not the task",
never accepted and ignored.

## A connection's hostd

`hostd` is a selector: a registered hostd's name (`ws-a`), or
`label=value[,label=value]` for any connected hostd carrying every label
(`pool=gpu`). Empty leaves the CLI where the framework's run location says
(container, host or detached, through the deployment's default hostd), as
every connection did before.

A run on a connection placed on a hostd:

- starts its agent CLI there, whatever the framework's run location
  (`hostdRoute.apply` makes it `host` for the run unless it is already
  `host` or `detached`);
- counts against that hostd's capacity (its allowlist's `capacity:`);
- keys a usage limit on an ambient credential (an Antigravity ambient
  login, a Codex subscription) to that hostd, so a limit hit on one
  machine does not pause the same connection on another;
- is recorded in `task_run_hostd` (hostd, instance, owner), shown on the
  run's timeline row as "ran on hostd ws-a on build-box · owner".

While the hostd is not connected the connection is **unavailable**: a run
pinned to the connection waits (`waiting for hostd ws-a`), and the rotation
passes the connection over (`N whose hostd cannot take a run now`). It never
falls back silently. `hostdFallback`, an ordered list of further selectors,
empty by default, is the only way a run on the connection goes elsewhere:
each is tried in order while the ones before it are away.

## A capability server's hostd

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
`run: host` with no `hostd` is the default hostd, as before. `hostd:` is a
name, a `k=v` selector string, or `{"name": ...}` / `{"labels": {...}}`.
Each entry is one instance on one hostd: the same server type on two hostds
is two entries under two capability ids.

A placed server is attached in its hostd's own registry, not the
deployment's; every run that holds a grant of it reaches it there, whatever
hostd the run's agent CLI went to. A run whose connection is on hostd A and
which holds a grant of a server on hostd B so starts its CLI on A and calls
the server on B. The run's `task_run_hostd` row says A; each of the
server's capability audit records (`task_run_capability_audit.hostd`) says
B. While a placed server's hostd is not connected, a grant of it cannot be
minted and the task waits with `needs-hostd:<selector>`.

## Migration

Schema 111 drops `task_hostd`, `template.hostd` and
`repo_config.default_hostd`. `task_run_hostd` is kept: it is the read-only
record of where each run's agent CLI ran, and the run history keeps
showing it. A deployment that pinned tasks or repositories to a hostd
moves the pin to the connection (or connections) those tasks ran on, in
Settings -> Models.
