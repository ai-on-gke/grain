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

## Enrolling and managing hosts

Remote hosts join in **Settings → Hosts** (`/settings/hosts`, alias `/settings/hostds`) or through `grain host`:

1. **Invite hosts (shared `--join` snippet).** Only the deployment owner can view, create, rotate, or revoke enrollment codes (`ghe_...`). Each code grants **nothing** except filing a 24-hour pending join request, and may carry an expiry, `max_uses`, `default_labels`, `default_capacity`, and `allowed_principals` (`user:<email>`, `group:<email>` resolved via `ui.RegisterGroupMemberChecker`, `github:<login>`, or `org:<org>`). The snippet uses `ui.RegisterHostdInstall` when an edition registers custom install instructions, and defaults to:
   ```sh
   curl -fsSL https://grain.example.com/hostd/install | sh -s -- --join <enrollment-code> --release <version>
   ```
   The snippet names the daemon's `--release` (and `--image`) when the daemon knows them, and says `--any-version` in the command when it does not: the script itself never skips its version check.
2. **Ed25519 join request and verified identity.** On first run the host generates an Ed25519 private key on disk (mode `0600`), signs a join request carrying its public key, hostname, OS/arch, detected agent CLIs, version, and local account, and presents a **verified** enrolling identity (via the deployment's `IdentityVerifier` / SSO proxy, `ui.RegisterHostJoinVerifier`, or a GitHub device-flow OAuth login verified server-side in OSS).
3. **Owner acceptance (owner-only, no auto-approve).** The request appears in **Settings → Hosts → Pending host requests** (`grain host pending`) with the verified person who enrolled it, the host's details and its key fingerprint, while the host's terminal waits. There is no pairing code to compare for now: the enrolling person is verified, so the owner checks who filed the request. Only the deployment owner may click **Accept** (`grain host accept <id> [-name -labels -capacity]`) or **Reject** (`grain host reject <id>`). Only the co-located `local` socket host (`/run/grain-hostd/hostd.sock`) operates without a handshake. Accepting sets the host's `Owner` to the verified enrolling identity — the exact owner that connection-level placement, IAP admission, and ambient-credential rate limits key on.
4. **30-day key-bound credential, silent renewal, and revocation.** Once accepted, the host claims a 30-day `ghd_...` credential bound to its Ed25519 public key. Every `/api/hostd/connect` upgrade must carry a fresh, un-replayed Ed25519 signature (`X-Grain-Hostd-Timestamp`, `X-Grain-Hostd-Nonce`, `X-Grain-Hostd-Signature`), so a copied token without the private key is refused. Connected key-bound hosts renew their 30-day credential automatically in-band (`OpRenew` / `POST /api/hostd/renew`), each renewal signed by the host's key and naming its current credential; legacy per-host tokens never renew. Revoking a host drops any live session immediately; offline and revoked hosts can be renamed or deleted cleanly (`grain host rename`, `grain host delete`).
5. **Migrating legacy per-host tokens.** Existing per-host token registrations continue working. Migrate an existing host in place without losing its name or placement references by re-running the installer with `--join <code>` or running `grain host rekey -server <url> -token-file <old-token-file> -key-file <new-key-file>` (or `grain-agentd install -rekey`) and approving the pairing code once in **Settings → Hosts**. Rekey is for migrating legacy tokens: once a host is key-bound, only a request signed by its bound key may rekey it (a copied credential cannot move it to another key, and a pending rekey with another key cannot be accepted after the host has been bound), so a host that has lost its key is revoked and re-enrolled with `--join`.

## Migration

Schema 111 drops `task_hostd`, `template.hostd` and
`repo_config.default_hostd`. `task_run_hostd` is kept: it is the read-only
record of where each run's agent CLI ran, and the run history keeps
showing it. A deployment that pinned tasks or repositories to a host
moves the pin to the connection (or connections) those tasks ran on, in
Settings -> Models.
