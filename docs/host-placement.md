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

## Revoking and deleting a host

Revoking a host (Settings -> Hosts -> **Revoke**,
`POST /api/hostds/{name}/revoke`) retires every token it has and drops its
live session: it is what cuts a host off. A revoked host stays in the
registry until it is deleted (Settings -> Hosts -> **Delete**, on revoked
rows only; `grain host delete <name>`; `DELETE /api/hostds/{name}`),
which removes its row and every `hostd_token` row it had, and records who
deleted it in the config audit (`hostd:<name>`), as revoking does.

- **Only a revoked host is deleted.** A live one is refused with "revoke
  it first", so deleting never cuts off a connected host or its running
  work by surprise. `local`, the co-located host, is never deleted, and an
  unknown name is a 404.
- **Configuration that names it refuses the delete.** A model connection
  whose `hostd` or `hostdFallback`, or a capability server entry whose
  `hostd`, names the host by name is listed in the refusal; point it at
  another host first. Clearing it silently would move that connection's
  runs, or that server, somewhere nobody chose. A label selector names no
  one host and does not hold a delete. (Per-task, template and repository
  pins went with schema 111, above, so there are none to check.)
- **Runtime state built from the name is cleared and reported.** The
  usage-limit pauses keyed `<credential>@hostd/<name>` are lifted and
  forgotten with the host, and the answer (and `grain host delete`)
  lists each one, so a host registered under the name later does not
  start out paused.
- **The run history keeps the name.** `task_run_hostd`, the capability
  audit's `hostd` and the spool cursors are not touched. A task page and
  `grain get` show the name each past run recorded, marked deleted when the
  registry no longer holds that host -- also once the name has been
  registered again, since that is a different host.
- **The name can be reused.** After deletion `POST /api/hostds` (or
  `grain host register`) with the same name registers a fresh host with
  a new token; nothing of the old row -- owner, labels, tokens -- carries
  over.

**Delete all revoked** in Settings -> Hosts (`grain host delete-revoked`,
`POST /api/hostds/delete-revoked`) deletes every revoked host nothing
still names and lists the ones it skipped, with why.

The `hostd` table is in the state repository's dump (its `hostd_token`
digests never are), so a deleted row leaves the dump at the next export.
