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

## Credentials: stored or ambient

Settings -> Models -> the connection -> **Credentials** says what a run on
the connection authenticates with:

- **Stored**: a key, token or `auth.json` grain holds for the connection
  (an API key, a Claude Code OAuth token, a Vertex AI service account, a
  ChatGPT subscription's uploaded `auth.json`), or reused from another
  connection.
- **Ambient**: the agent CLI's own login on the machine its CLI runs on
  (`authMode: ambient`, for Antigravity, Claude and Codex alike). grain
  stores no secret for it.

Where the connection's CLI starts through grain hostd (its framework runs
`host` or `detached` on a daemon whose host executor is grain hostd),
ambient is accepted only on a connection placed on a host (`hostd`), so
which machine's login a run spends is always said rather than implied by
the default host (`model.CheckAmbientPlacement`). A save that makes such a
connection ambient with no host, or clears the host of an ambient one, is
refused with "ambient credentials are the agent CLI's own login on the
machine it runs on ...". A CLI the daemon starts as its own child -- in its
container, or on the bare machine of a development daemon -- has one login
an unplaced ambient connection can mean, the daemon's own, and is not
refused by the API. Settings always asks for the host when Ambient is
chosen. A connection saved ambient and unplaced before the rule keeps
running through the default host, untouched by unrelated saves, until it
is changed; where there is only one host it is placed on it (below).

A Codex *subscription* connection counts as stored here: grain holds its
`auth.json`. (A run of one on a host with a `codex login` of its own still
links that login, so a usage limit on it is keyed to the host the way an
ambient one is.)

## Which logins a host has

A host reports the agent CLI logins on its machine, as the user it runs
as, in its hello (`hostd.Hello.Logins`), configured by an optional top-level
`logins:` map (`name -> path`, `~/` allowed) in its allowlist. When `logins:`
is omitted, the built-in table applies: `codex` for `$CODEX_HOME/auth.json`
(else `~/.codex/auth.json`), `claude` for
`$CLAUDE_CONFIG_DIR/.credentials.json` (else
`~/.claude/.credentials.json`), and `antigravity` for
`~/.gemini/oauth_creds.json` or agy's own `~/.gemini/antigravity-cli`. The
daemon asks a dialing-in host once it connects, and the co-located one
when the list is read (`Has(FeatureLogins)` distinguishes an empty list,
meaning none, from a host too old to report them); `GET /api/hostds` carries
them as `logins` (with `loginsKnown`), and the **Run on host** picker shows
them beside each host ("logins: codex, claude", "no CLI logins", or nothing
for a host that is offline or too old to say). A file being there says the
CLI signed in on that machine, not that the login is still good.

## Host work nothing places

A `run: host` capability server with no `hostd`, and a connection with no
`hostd` whose CLI runs on the host because Settings -> Models says
**Host** or the daemon's `-<cli>-run` flag (`GRAIN_*_RUN=host`, or an
edition's built-in default) does, go to the deployment's default host.
When that is the only host there is -- exactly one unrevoked host in the
registry (`local` counting only where a co-located grain hostd runs), and
it is the default -- grain writes it down
(`model.MigrateImplicitHostPlacement`): the server becomes `run: hostd`,
`hostd: <host>`, and the connection gets `hostd: <host>`. It does this when
the daemon starts and on every grain/config import (`pkg/staterepo`), so
the next export carries the placement. With several hosts nothing is
rewritten: which one was meant stays the default host's. A connection
whose CLI runs in the daemon's container, or detached, is left where it
is. A development daemon on the bare machine, whose host runs are its own
children, has no host to write down and rewrites nothing.

## Enrolling and managing hosts

Remote hosts join in **Settings → Hosts** (`/settings/hosts`, alias `/settings/hostds`) or through `grain host`:

1. **Invite hosts (shared `--join` snippet).** Only the deployment owner can view, create, rotate, or revoke enrollment codes (`ghe_...`). Each code grants **nothing** except filing a 24-hour pending join request, and may carry an expiry, `max_uses`, `default_labels`, `default_capacity`, and `allowed_principals` (`user:<email>`, `group:<email>` resolved via `ui.RegisterGroupMemberChecker`, `github:<login>`, or `org:<org>`). The snippet uses `ui.RegisterHostdInstall` when an edition registers custom install instructions, and defaults to:
   ```sh
   curl -fsSL https://grain.example.com/hostd/install | sh -s -- --join <enrollment-code> --release <version>
   ```
   The snippet names the daemon's `--release` (and `--image`) when the daemon knows them, and says `--any-version` in the command when it does not: the script itself never skips its version check.
2. **Ed25519 join request and verified identity.** On first run the host generates an Ed25519 private key on disk (mode `0600`), signs a join request carrying its public key, hostname, OS/arch, detected agent CLIs, version, and local account, and presents a **verified** enrolling identity (via the deployment's `IdentityVerifier` / SSO proxy, or a GitHub device-flow OAuth login verified server-side in OSS).
3. **Owner acceptance (owner-only, no auto-approve).** The request appears in **Settings → Hosts → Pending host requests** (`grain host pending`) with the verified person who enrolled it, the host's details and its key fingerprint, while the host's terminal waits. There is no pairing code to compare for now: the enrolling person is verified, so the owner checks who filed the request. Only the deployment owner may click **Accept** (`grain host accept <id> [-name -labels -capacity]`) or **Reject** (`grain host reject <id>`). Only the co-located `local` socket host (`/run/grain-hostd/hostd.sock`) operates without a handshake. Accepting sets the host's `Owner` to the verified enrolling identity — the exact owner that connection-level placement, IAP admission, and ambient-credential rate limits key on.
4. **30-day key-bound credential, silent renewal, and revocation.** Once accepted, the host claims a 30-day `ghd_...` credential bound to its Ed25519 public key. Every `/api/hostd/connect` upgrade must carry a fresh, un-replayed Ed25519 signature (`X-Grain-Hostd-Timestamp`, `X-Grain-Hostd-Nonce`, `X-Grain-Hostd-Signature`), so a copied token without the private key is refused. Connected key-bound hosts renew their 30-day credential automatically in-band (`OpRenew` / `POST /api/hostd/renew`), each renewal signed by the host's key and naming its current credential; legacy per-host tokens never renew. Revoking a host drops any live session immediately; any host can be renamed (`grain host rename`), and a revoked one can be deleted, which frees its name (`grain host delete`, or `grain host delete-revoked` and Settings -> Hosts -> Delete all revoked for every revoked host nothing still names). An offline host that should go is revoked first, then deleted.
5. **Migrating legacy per-host tokens.** Existing per-host token registrations continue working. Migrate an existing host in place without losing its name or placement references by re-running the installer with `--join <code>` or running `grain host rekey -server <url> -token-file <old-token-file> -key-file <new-key-file>` (or `grain-agentd install -rekey`) and approving the pairing code once in **Settings → Hosts**. Rekey is for migrating legacy tokens: once a host is key-bound, only a request signed by its bound key may rekey it (a copied credential cannot move it to another key, and a pending rekey with another key cannot be accepted after the host has been bound), so a host that has lost its key is revoked and re-enrolled with `--join`.

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
