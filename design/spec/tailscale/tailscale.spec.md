# Host Tailscale Capability Specification

**Status:** Normative specification for [`design/design/tailscale-host.design.md`](../../design/tailscale-host.design.md). **Not yet implemented.** It builds on the capability protocol in [`capabilities.spec.md`](../capabilities/capabilities.spec.md). Section references written `§n` without a file name are to that document, and references written `TS-n` are to this one. Where this document and the capability spec disagree on a Tailscale matter, this document wins. On protocol matters, the capability spec wins.

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT" and "MAY" are to be read as in BCP 14 (RFC 2119, RFC 8174) when they appear in all capitals.

## Contents

1. [Components](#1-components)
2. [Host policy](#2-host-policy-etcgraintailscaleyaml)
3. [Server configuration (`configure`)](#3-server-configuration-configure)
4. [Install](#4-install)
5. [Node options and apply](#5-node-options-and-apply)
6. [Operator operations (wire level)](#6-operator-operations-wire-level)
7. [Agent method `#tailscale-status`](#7-agent-method-tailscale-status)
8. [Teardown](#8-teardown)
9. [Audit](#9-audit)
10. [Failure modes](#10-failure-modes)
11. [Security rules](#11-security-rules)
12. [Out of scope](#12-out-of-scope)

---

## 1. Components

**TS-1. Server.** The capability server is the in-tree `grain capability tailscale`, registered as a built-in under the canonical ID `https://grain.dev/capabilities/tailscale`. It is in `needs-config` until it attaches with a host policy present (TS-10).

**TS-2. Host only.** The server MUST run `run: host` through grain host, from the allowlist entry `grain-tailscale` (`<grain> capability tailscale`, no `extra_args`, no `env`). That entry is added to `intree.HostdBuiltins`, so `grain-hostd-allowlist.service` and hostd's own offers provide it. A container-run entry MUST fail to attach with `tailscale runs on the host only (run: host)`. The node it manages is the node of the host where that hostd runs.

**TS-3. Root helper.** Every action that reads or changes Tailscale or apt state is done by `grain tailscale-helper`, a root process run as `grain-tailscale-helper.service`. That service is socket-activated by `grain-tailscale-helper.socket` on `/run/grain-tailscale/helper.sock` (`SocketUser=<GRAIN_USER>`, `SocketMode=0600`). The server MUST NOT execute `tailscale`, `tailscaled`, `apt-get`, `apt-mark`, `dpkg`, `gpg` or `systemctl` itself.

**TS-4. Helper binary.** The unit's `ExecStart` MUST name a root-owned binary that only root can write: `/usr/local/lib/grain/tailscale-helper/grain`, installed by `scripts/setup.sh` from the release it installs. The helper MUST refuse to start if that file, or any directory above it, is writable by a non-root user.

**TS-5. Helper sandboxing.** The unit runs with `ProtectSystem=strict`, `ReadWritePaths=/var/lib/tailscale /var/lib/apt /var/lib/dpkg /var/cache/apt /usr/share/keyrings /etc/apt/sources.list.d /etc/apt/preferences.d /run/grain-tailscale`, `PrivateTmp=yes`, `RuntimeDirectory=grain-tailscale/keys` (mode `0700`), `NoNewPrivileges=yes`, and `ProtectHome=yes`. The `dpkg` install additionally needs whatever paths the `tailscale` package writes (`/usr/bin`, `/usr/sbin`, `/lib/systemd/system`, `/etc/default`). The unit MUST list those paths explicitly and MUST NOT disable `ProtectSystem`.

**TS-6. Helper protocol.** The helper accepts one request per connection: a JSON object `{"op": <op>, "request": {...}}` of at most 64 KiB. It answers with one JSON object `{"ok": bool, "code"?: string, "message"?: string, "result"?: {...}, "log"?: [string]}`. Valid values of `op` are `status`, `plan`, `install`, `apply`, `down` and `logout`. Any other value is refused with `code: "unknown_op"`. The helper MUST check `SO_PEERCRED` and refuse any peer whose uid is neither `GRAIN_USER`'s nor root's.

**TS-7. Fixed argv.** The helper runs commands only as argv arrays, never through a shell. Each array is built from constants and from values that have passed TS-11/TS-30 validation. The complete set of programs it may run is `tailscale`, `apt-get`, `apt-cache`, `apt-mark`, `gpg`, `systemctl` and `dpkg-query`, each at an absolute path under `/usr/bin` or `/usr/sbin`. Its subcommands are exactly those named in §4, §5, §7 and §8.

**TS-8. Re-validation.** The helper MUST validate every request against the host policy (§2) as it reads it at that moment, whatever the server has already checked. The helper is the security boundary, and the server only shapes requests.

**TS-9. Concurrency.** The helper serializes `install`, `apply`, `down` and `logout` under one lock (`/run/grain-tailscale/op.lock`). A second mutating request while one is running gets `code: "busy"`. `status` and `plan` do not take the lock.

## 2. Host policy (`/etc/grain/tailscale.yaml`)

**TS-10. Presence and ownership.** The host policy is a YAML file at `/etc/grain/tailscale.yaml`. It MUST be a regular file, not a symlink, owned by root and not writable by group or other. A file that fails any of these checks is treated as absent. While it is absent, every `op` except `status` is refused with `code: "no_host_policy"`, and `status` honours `status_peers: false`.

**TS-11. Schema.** Unknown keys are refused, so the whole file is invalid and is treated as absent, with the reason in `status`.

```yaml
# Exact Debian package versions this host may be installed at. Empty or
# missing: install is refused (no_version_allowed).
versions: ["1.80.3", "1.82.0"]
# Full 40-hex-digit OpenPGP fingerprint of pkgs.tailscale.com's archive key.
# REQUIRED for install.
signing_key_fingerprint: "<40 hex digits, upper case, no spaces>"
# Tags configuration may request. Each matches ^tag:[a-z0-9][a-z0-9-]{0,62}$.
allow_tags: ["tag:grain"]
# Hostnames configuration may request (anchored RE2). Default: ^[a-z0-9][a-z0-9-]{0,62}$
hostname_pattern: "^grain-[a-z0-9-]{1,40}$"
allow_ssh: false          # may configuration turn on --ssh
allow_exit_node: false    # may configuration turn on --advertise-exit-node
# CIDRs configuration may advertise; each configured route must be equal to
# or contained in one of these. Empty: no route may be advertised.
allow_routes: []
status_peers: false       # may status include peers
```

- `versions` entries match `^[0-9]+\.[0-9]+\.[0-9]+$`.
- `allow_routes` entries are canonical IPv4 or IPv6 prefixes. The default routes `0.0.0.0/0` and `::/0` MUST be refused here, because advertising them is what `allow_exit_node` decides.
- There is no key for the repository URL or the track. Both are fixed by TS-21.

**TS-12. Who writes it.** Grain never writes this file. A person with root on the host writes it. `scripts/setup.sh` MAY create a minimal file (`status_peers: false` and nothing else) when none exists, and MUST NOT change one that does, the same rule hostd's allowlist follows.

**TS-13. Live reads.** The helper reads the policy on every request, so a change takes effect without a restart. Settings shows the policy as the helper last read it (`plan` result `policy`).

**TS-14. Policy narrowing.** A policy change can leave the node outside the policy, for example SSH turned on and then `allow_ssh: false`. The helper MUST NOT change the node on its own because of that. `plan` reports `outside_policy` items, and an `apply` with exposure-narrowing changes brings the node back inside it.

## 3. Server configuration (`configure`)

**TS-15. Schema.** The server advertises `configure` (§4.5) with this configuration schema:

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "version":  { "type": "string", "pattern": "^[0-9]+\\.[0-9]+\\.[0-9]+$", "description": "Package version to install; must be in the host policy's versions." },
    "hostname": { "type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,62}$", "description": "Tailnet hostname. Empty or absent: leave as is." },
    "tags":     { "type": "array", "items": { "type": "string", "pattern": "^tag:[a-z0-9][a-z0-9-]{0,62}$" }, "uniqueItems": true, "maxItems": 16, "default": [] },
    "ssh":                 { "type": "boolean", "default": false },
    "advertise_exit_node": { "type": "boolean", "default": false },
    "advertise_routes":    { "type": "array", "items": { "type": "string", "maxLength": 43 }, "uniqueItems": true, "maxItems": 32, "default": [] },
    "auth_key": { "type": "string", "writeOnly": true, "description": "Tailnet auth key; used only when the node logs in." }
  }
}
```

**TS-16. No host effect.** `configure` MUST validate the configuration against the schema and against the host policy (through the helper's `plan`), record it as the desired state, and return `{"status": "ok"}`. It MUST NOT install, log in, or change any node pref. Grain calls `configure` on every attach (§4.5.4), and a restart must not change the network. A configuration outside the host policy fails `configure` (§4.5.5), naming the field and the policy key. A host with no policy, or with no Tailscale installed, is not a `configure` failure; Settings reports it through `plan`.

**TS-17. Auth key handling.** `auth_key` is a secret field (§4.5.2), stored only in grain's secret store and referenced from `config` as `$secret:<name>`. The server holds the resolved value in memory only. It MUST send the value to the helper only in an `apply` request body, and only when that apply logs the node in (TS-34). It MUST NOT write the value to disk, log it, include it in an error, an audit record or a tool result, or pass it in any argv. The server and the helper MUST scrub the value (and any `tskey-` token) out of any text they return.

**TS-18. Missing key.** `auth_key` is optional, and a configuration without it is valid, because the key is needed only for a login (TS-81). A *reference* whose secret has been deleted is handled as §4.5.5 says: the server is unhealthy until a person sets the secret or clears the field. The node itself is unaffected either way (TS-97).

**TS-19. Rotation.** To rotate the key, a person (1) creates a new key in the Tailscale admin console, (2) saves it into `auth_key` in Settings, which re-attaches and re-configures with no host effect (TS-16), and (3) revokes the old key in the admin console. A running node is unaffected, and the next login uses the new key. The server MUST NOT cache a key across a re-configure.

## 4. Install

**TS-20. Inputs.** `install` installs `config.version`. It MUST be refused with `code: "version_not_allowed"` unless that version is in the host policy's `versions`, and with `no_version_configured` when it is unset. The request carries no repository, URL, codename or version of its own.

**TS-21. Repository.** The source is fixed at `https://pkgs.tailscale.com/stable/debian/<codename>`. `<codename>` is `VERSION_CODENAME` from `/etc/os-release` and MUST match `^[a-z]+$`. A host whose `ID` is not `debian`, and whose `ID_LIKE` does not contain `debian`, is refused with `unsupported_host`. Ubuntu and other derivatives are out of scope (TS-120). The helper fetches over HTTPS with system CAs only. It MUST NOT use grain's proxy cache or trust grain's interception CA ([`docs/package-cache.md`](../../../docs/package-cache.md)).

**TS-22. Key verification.** The helper fetches `<base>.noarmor.gpg` (at most 64 KiB) into its private tmp and runs `gpg --show-keys --with-colons --with-fingerprint` against a throwaway `GNUPGHOME`. The file MUST contain exactly one primary key, whose fingerprint equals the policy's `signing_key_fingerprint`. Otherwise install fails with `signing_key_mismatch` and nothing is written.

**TS-23. Source written, not fetched.** The helper writes the verified key to `/usr/share/keyrings/tailscale-archive-keyring.gpg` (`0644`). It writes `/etc/apt/sources.list.d/tailscale.list` (`0644`) with the exact content `deb [signed-by=/usr/share/keyrings/tailscale-archive-keyring.gpg] https://pkgs.tailscale.com/stable/debian <codename> main\n`. The upstream `.tailscale-keyring.list` MUST NOT be fetched or used.

**TS-24. Coexistence with `setup.sh`.** `setup_tailscale` writes the same two paths. If both already exist, the keyring's fingerprint matches the policy, and the list's single `deb` line matches TS-23 apart from whitespace, the helper keeps them as they are. If either names another key or another URL, install fails with `foreign_apt_source` and changes nothing. A person resolves that by hand. Install never edits any other apt source.

**TS-25. Fetch and select.** The helper refreshes only that source: `apt-get update -o Dir::Etc::sourcelist=sources.list.d/tailscale.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0`. It then reads `apt-cache madison tailscale`. If `config.version` is not listed, install fails with `version_unavailable` (TS-82).

**TS-26. Install and hold.** The helper runs `apt-get install -y --no-install-recommends tailscale=<version>` with `DEBIAN_FRONTEND=noninteractive`, then `apt-mark hold tailscale`, then `systemctl enable --now tailscaled`. A version lower than the installed one requires `acknowledge` to contain `downgrade` (TS-36). An installed version equal to `config.version` that is already held returns `result.changed: false` and runs nothing after TS-25. The hold is intentional: the version changes only through this operation or by a person with root. `plan` says so.

**TS-27. Result.** `install` returns `{installed_version, previous_version, held: true, tailscaled: "active"|..., changed}`. A failure after `apt-get install` has started reports the state `dpkg-query` shows afterwards. It is never rolled back automatically.

## 5. Node options and apply

**TS-30. Closed mapping.** Each configuration field maps to exactly one flag. No other flag is ever passed:

| Field | `tailscale up` / `tailscale set` flag | Host policy gate |
|---|---|---|
| `hostname` | `--hostname=<v>` | `hostname_pattern` |
| `tags` | `--advertise-tags=<comma-joined>` (`up` only) | each in `allow_tags` |
| `ssh` | `--ssh=<bool>` | `allow_ssh` when `true` |
| `advertise_exit_node` | `--advertise-exit-node=<bool>` | `allow_exit_node` when `true` |
| `advertise_routes` | `--advertise-routes=<comma-joined canonical prefixes>` | each within `allow_routes` |
| `auth_key` | `--auth-key=file:/run/grain-tailscale/keys/<random>` (`up` only, on login) | none |

**TS-31. No pass-through.** No request, configuration field, grant argument or tool argument may carry a `tailscale` flag, subcommand or argument string. Every value is validated as data (pattern, CIDR parse, membership) before it is formatted into the one flag that TS-30 names for it.

**TS-32. Forbidden.** The helper MUST NOT run `tailscale serve`, `funnel`, `cert`, `file`, `drive`, `web`, `switch`, `configure`, `update` or `debug` (apart from `debug prefs`, used read-only by `plan`). It MUST NOT pass `--reset`, `--force-reauth` (except as TS-34 allows), `--accept-routes`, `--exit-node`, `--exit-node-allow-lan-access`, `--shields-up`, `--operator`, `--accept-dns`, `--netfilter-mode`, `--login-server` or `--unattended`. `serve` and `funnel` state, which `setup_tailscale` owns, MUST be left exactly as it was.

**TS-33. Ceiling.** Before running anything, `apply` validates the desired state against the host policy. Any value outside it fails with `outside_policy`, naming the field and the policy key. Exposure-*narrowing* values (turning `ssh` or `advertise_exit_node` off, removing a route or tag) are always allowed.

**TS-34. State machine.** `apply` reads `BackendState` from `tailscale status --json --peers=false`:
- `NeedsLogin` or `NoState` (logged out): login. The request MUST carry the key (TS-17), or the apply fails with `needs_auth_key`. The helper writes the key to `/run/grain-tailscale/keys/<random>` (`0600`, tmpfs) and runs `tailscale up --auth-key=file:<path>` with every TS-30 flag for the desired state. It unlinks the file whether the call succeeds or fails, and in any case before the response is sent.
- `Running`, `Starting`: prefs change. The helper runs `tailscale set` with only the TS-30 flags whose value differs from the current prefs. A `tags` difference cannot be applied with `set`. It requires `acknowledge` to contain `reauth` and the key, and then runs `tailscale up --force-reauth --auth-key=file:<path>` with every TS-30 flag. Without both it fails with `needs_reauth`.
- `Stopped`: the node is logged in but down. The helper applies the prefs as for `Running`, then runs `tailscale up` with every TS-30 flag and no key. It brings the node up only when `acknowledge` contains `up`.
- `NeedsMachineAuth`: fails with `needs_machine_auth`. A person approves the node in the admin console.

**TS-35. Adoption and unmanaged prefs.** On first attach the node may have been brought up by `setup_tailscale` or by hand. The server treats the current node as is. `plan` reports each TS-30 field where the node differs from the configuration as `drift`, and nothing changes until a person applies. A field absent from the configuration (for example no `hostname`) is not managed and is never changed. When `tailscale up` refuses because a non-default pref outside TS-30 is set ("requires mentioning all non-default flags"), the apply fails with `unmanaged_prefs`, quoting the flags tailscale named. The helper MUST NOT retry with `--reset` or with those flags.

**TS-36. Acknowledgements.** An `apply` or `install` request carries `acknowledge: [string]`. Every item `plan` lists as required for the change MUST be present, or the operation fails with `needs_acknowledgement`, listing the missing items, before anything runs:

| Item | Required when |
|---|---|
| `ssh` | `ssh` goes from off to on |
| `exit-node` | `advertise_exit_node` goes from off to on |
| `route:<prefix>` | a route is added (one item per prefix) |
| `tags` | the advertised tags change (the node's ACL identity changes) |
| `reauth` | the change needs a re-login (TS-34) |
| `rename` | `hostname` changes (TS-37) |
| `up` | a `Stopped` node is brought up |
| `downgrade` | install moves to a lower version |
| `down` / `logout` | every `tailscale_down` / `tailscale_logout` (TS-61, TS-62) |
| `serve` | a `down` or `logout` while `serve_configured` is true (TS-62) |

Settings MUST present each required item as its own unchecked checkbox, worded from `plan`'s `explain` text. It MUST NOT offer a "select all" control.

**TS-37. Rename.** A hostname change changes the node's MagicDNS name, and so the `*.ts.net` URL where `setup_tailscale` serves the UI. `pkg/ui/hostguard.go` accepts any `*.ts.net` Host, so no change is needed there. `plan`'s `explain` for `rename` MUST state the old and new names and say that the UI's tailnet URL changes.

**TS-38. Key never in argv.** The auth key MUST reach `tailscale` only through `--auth-key=file:<path>`. A Tailscale version whose `up` does not support the `file:` prefix MUST NOT be added to `versions`. The helper checks this at install by parsing `tailscale up --help` and fails with `unsupported_version` if the check fails.

**TS-39. Apply result.** `{backend_state, changed: [field], ran: [argv with the key path replaced by "<auth-key>"], status}`, where `status` is a TS-53 status document taken after the change.

## 6. Operator operations (wire level)

**TS-40. Control-plane tools.** The server advertises these MCP tools beside `configure` and the TS-50 mint/release pair. They are not capability methods: they are never granted, and they are discovered the way `host-script`'s `test_script` is.

| Tool | `inputSchema` (all `additionalProperties: false`) | Effect |
|---|---|---|
| `tailscale_plan` | `{requested_by}` | Read-only. Returns `{policy, installed, desired, current, drift[], outside_policy[], actions[], acknowledgements_required[{item, explain}]}` for both install and apply |
| `tailscale_install` | `{requested_by, acknowledge}` | §4 |
| `tailscale_apply` | `{requested_by, acknowledge}` | §5 |
| `tailscale_down` | `{requested_by, acknowledge}` | TS-61 |
| `tailscale_logout` | `{requested_by, acknowledge}` | TS-62 |

`requested_by` is `{"kind": "person", "id": <grain identity>}`. `acknowledge` is an array of strings, each at most 64 bytes and at most 64 items, matching `^[a-z-]+(:[0-9a-f:./]+)?$`. A tool result is the helper's response (TS-6) with `log` capped at 64 KiB. An `ok: false` result is returned with `isError: true`. Its `code` is one of §10's.

**TS-41. Timeouts.** `tailscale_install` may take up to 10 minutes, `tailscale_apply` up to 2 minutes, and the others up to 30 seconds. Grain's call timeout for these tools MUST cover those limits, unlike a mint's (§4.0.2). The helper enforces its own limits and kills its child process group when one passes.

**TS-42. Routes.** Grain exposes the tools as `POST /api/capability-servers/tailscale/{plan|install|apply|down|logout}` with body `{"server": <capability_id>, "acknowledge": [...]}`. Grain fills in `requested_by` from the authenticated session. The body cannot set it.

**TS-43. Settings.** The server's card shows the `plan` result: installed and desired version, backend state, drift, and outside-policy items. It offers **Install**, **Apply**, **Bring down** and **Log out**, each opening the TS-36 acknowledgement list from a fresh `plan`.

**TS-44. Configuration first.** `install` and `apply` act only on the configuration as last passed to `configure`. A request MUST NOT carry desired-state fields.

**TS-45. Person only.** The TS-42 routes need scope `capabilities:admin` (dangerous; [`docs/agent-api.md`](../../../docs/agent-api.md)). They MUST be refused (403) to every agent API caller, even one whose task holds that scope, and to CI job tokens. They are not offered as `api_*` tools. `tailscale_plan` follows the same rule, because its result includes the host policy.

**TS-46. No scheduling.** Grain MUST NOT call `tailscale_install`, `tailscale_apply`, `tailscale_down` or `tailscale_logout` except in response to a TS-42 request. It never calls them on attach, upgrade, restart, schedule or task completion.

## 7. Agent method `#tailscale-status`

**TS-50. Method.** `mint_tailscale-status` / `release_tailscale-status` follow the attempt-scoped MCP toolset pattern (§6.2). The `with:` schema is `{"include_peers": {"type": "boolean", "default": false}}` with `additionalProperties: false`. Mint registers `/runs/<attempt_id>/mcp`, serving one tool, `tailscale_status` (input `{}`), and returns `{status: "ok", mcp_endpoint, prompt_section}`. The prompt section says the node is read-only from the agent's side and that changes need a person in Settings. Mint is idempotent per `attempt_id` (§4.3.5), expires at `deadline` plus 5 minutes, and does not call the helper.

**TS-51. Person-granted.** The server registers `#tailscale-status` with `protocol.RequirePersonGrant` (`intree.Builder.PersonGranted`), so no parent grant or `propose_task` covers it (§2.3.4). Its tool MUST be model-only (`modelOnly: true`), never delivered through `grain cap` and never placed on the sandbox CLI credential (§5.2.6).

**TS-52. Peers.** `include_peers: true` MUST be refused at mint (`refused`, reason naming `status_peers`) when the host policy says `status_peers: false`. The server checks this at mint and the helper checks it again on every call.

**TS-53. Status document.** `tailscale_status` returns, in `structuredContent`:

```json
{
  "version": "1.82.0",
  "backend_state": "Running",
  "health": ["..."],
  "self": {
    "hostname": "grain-prod", "dns_name": "grain-prod.example.ts.net",
    "tailscale_ips": ["100.64.0.7", "fd7a:115c:a1e0::7"],
    "tags": ["tag:grain"], "online": true,
    "ssh": false, "advertise_exit_node": false, "advertise_routes": [],
    "key_expiry": null
  },
  "serve_configured": true,
  "peers": [ { "hostname": "...", "dns_name": "...", "os": "linux", "online": true, "tags": [], "tailscale_ips": ["..."] } ]
}
```

`peers` is present only under TS-52. The helper derives the document from `tailscale status --json` (with `--peers=false` unless peers are allowed) and `tailscale debug prefs`, copying only these fields. It MUST NOT include node, machine, disco or auth keys, login names or emails, user IDs, the control URL, the tailnet's other DNS settings, or the content of the serve configuration (only `serve_configured`).

**TS-54. Limits.** At most 600 `tailscale_status` calls per grant. Further calls return `isError: true` with `call limit reached` and are counted, not recorded individually (TS-72).

**TS-55. Expiry warning.** When `self.key_expiry` is less than 14 days away, `health` contains `node key expires <time>`, and Settings shows it on the card.

**TS-56. No mutation.** No tool served under `#tailscale-status` changes any state, and no future argument may make one do so. A method that changes the node is a new method, under a new design.

## 8. Teardown

**TS-60. Release never touches the node.** `release_tailscale-status` unregisters the attempt's endpoint (later requests get `404`) and returns `{status: "ok", audit, audit_omitted}` (§4.4.4). It is idempotent. It MUST NOT log out, bring down, re-configure or otherwise affect the node. No attempt ending, of any kind, changes the node.

**TS-61. Down.** `tailscale_down` runs `tailscale down`. The node stays logged in (state `Stopped`), and `apply` with `up` brings it back without a key.

**TS-62. Logout.** `tailscale_logout` runs `tailscale logout`. The node's key is invalidated and, if the node is ephemeral, the node is removed from the tailnet. A later `apply` needs an auth key. `tailscale_down` requires `acknowledge` to contain `down`, and `tailscale_logout` requires `logout`. When `serve_configured` is true, they also require `serve`, whose `explain` says the UI stops being reachable on the tailnet, and may cut off the session making the request if it came in over the tailnet.

**TS-63. Uninstall.** There is no uninstall operation. `apt-mark unhold tailscale && apt-get purge tailscale` is done by hand, as `setup.sh`'s uninstall notes already say.

**TS-64. Server removal.** Disabling or removing the server entry, or uninstalling grain, MUST NOT change the node.

## 9. Audit

**TS-70. Helper journal.** The helper writes one line per request to its journal (`journalctl -u grain-tailscale-helper`). The line holds `op`, peer uid, `requested_by` as relayed, `acknowledge`, each argv it ran (key path replaced by `<auth-key>`), exit codes, `code` and duration. It contains no key.

**TS-71. Server log.** The server appends one JSON record per call, refused calls included, to `<data-dir>/audit.jsonl` (default `~/.cache/grain/tailscale`, `0600`). Calls covered are `configure`, every `tailscale_*` control tool, `mint_`/`release_tailscale-status` and every `tailscale_status`. A record has `time`, `tool`, `caller` and `outcome` (`ok` or the error `code`). `caller` is `grain` for `configure`, mint and release, `person:<id>` for operator tools, and `model` for `tailscale_status`. Records for operator tools also carry `acknowledge` and `changed`. Records for status calls carry `attempt_id` and `include_peers`. No record carries a status document, a key or a peer list.

**TS-72. Grain store.** For each TS-42 request grain writes a `model.CredentialAccessEntry` with `Kind: "tailscale"`, `Resource: <capability_id>`, `Action: <op>`, `Outcome` set to `ok` or the `code`, and the person's identity. Recording the identity needs a new `Actor` field on the entry. A `tailscale_apply` or `tailscale_logout` whose request held a key also records `Credential: <secret name>`, never its value. Status calls reach the attempt's run record through the release audit (§4.4.4), at most 4096 records with the excess in `audit_omitted`.

**TS-73. Retention.** `audit.jsonl` rotates at 16 MiB and keeps 4 files. The store log follows the credential-access log's retention.

**TS-74. Who reads it.** The store copy is shown where credential access is shown, to people with `capabilities:admin`, and on the attempt view for status calls. Records hold no secrets (TS-17), so they need no redaction.

## 10. Failure modes

Every failure is a helper or server `code`, surfaced as an `isError` tool result, a 4xx/5xx on TS-42 routes with `{code, message}`, or (at mint) `refused` with that reason.

**TS-80. Already up (`adopted`, not an error).** A node that is `Running` before the server is ever configured is adopted under TS-35. It is never re-logged in, `plan` reports its drift, and `apply` changes only the drifted, managed, allowed fields. A node that some other agent or person changes outside grain shows up as drift on the next `plan`.

**TS-81. Lost or missing auth key (`needs_auth_key`).** Status, `plan`, `down`, and `apply` on a logged-in node without a tag change keep working. A login or re-auth fails before anything runs, with the message "set auth_key in Settings (a new key from the Tailscale admin console)". An expired or revoked key is rejected by `tailscale up`. The helper maps that output to `auth_key_rejected`, scrubbed under TS-17, and the node's state is unchanged.

**TS-82. Version no longer served (`version_unavailable`).** The message lists the policy's versions that `apt-cache madison` still shows. The installed package, its hold and the node are untouched. The helper MUST NOT fall back to another version. A person adds a served version to the host policy, sets it in configuration and installs again. If no version is installed at all, `status` reports `installed: null`.

**TS-83. No systemd (`unsupported_host`).** Without systemd as PID 1 (no `/run/systemd/system`), the helper unit cannot exist. `setup.sh` MUST NOT install the units, and the server, unable to connect to the socket, attaches in `needs-config` with reason `unsupported_host: grain-tailscale needs systemd`. There is no fallback to `tailscaled --tun=userspace-networking` or to running `tailscale` as the operator's uid.

**TS-84. No helper (`helper_unavailable`).** On a systemd host, if the socket is missing or refuses the connection, the server reports `needs-config` with the command to fix it (`sudo systemctl enable --now grain-tailscale-helper.socket`). `tailscale_status` returns `isError` with the same text.

**TS-85. Not installed (`not_installed`).** `apply`, `down` and `logout` fail, and `status` returns `{installed: null, backend_state: null}`.

**TS-86. tailscaled not running (`daemon_down`).** `status` reports `backend_state: null` with a `health` line. `apply` fails, and the helper does not start tailscaled outside `install`.

**TS-87. apt failures.** `apt_locked` (dpkg lock held, retryable), `fetch_failed` (network/TLS), `signing_key_mismatch` (TS-22), `foreign_apt_source` (TS-24), `install_failed` (apt-get exit code and the last 4 KiB of its output).

**TS-88. Other codes.** `no_host_policy`, `no_version_allowed`, `no_version_configured`, `version_not_allowed`, `outside_policy`, `needs_acknowledgement`, `needs_reauth`, `needs_machine_auth`, `unmanaged_prefs`, `unsupported_version`, `busy`, `unknown_op`, `timeout`.

## 11. Security rules

These restate, as one checklist for reviewers, rules made normative above. An implementation MUST meet all of them.

**TS-90.** No agent changes the node: the only granted method is read-only (TS-56), and every changing operation is a person-only route (TS-45, TS-46).
**TS-91.** No free-form arguments reach `tailscale` or `apt` from anywhere (TS-7, TS-31, TS-32).
**TS-92.** The root helper re-validates every request against a root-owned host policy that grain cannot write (TS-8, TS-10, TS-12).
**TS-93.** Each exposure-widening change (SSH, exit node, routes, tags, rename) is allowed by host policy, set in configuration, and acknowledged item by item in the request (TS-33, TS-36).
**TS-94.** The auth key lives only in grain's secret store, travels only in memory and over the `0600` socket, reaches `tailscale` only through a tmpfs `file:` reference, and never appears in argv, logs, errors, audit records, grants or tool results (TS-17, TS-34, TS-38).
**TS-95.** The install source is fixed, its key fingerprint is pinned and verified before use, the source line is written rather than fetched, and the version is pinned and held (TS-21..TS-26).
**TS-96.** The status document leaves out credentials and identities, and leaves out peers unless policy allows them (TS-52, TS-53).
**TS-97.** Nothing in grain's lifecycle (attach, restart, upgrade, attempt end, server removal) changes the node or its `serve` state (TS-16, TS-32, TS-46, TS-60, TS-64).

## 12. Out of scope

**TS-120.** Out of scope for this specification:
- uninstalling Tailscale;
- Ubuntu and other non-Debian distributions;
- the `unstable` track;
- tailnet ACLs and auth-key minting through the Tailscale API;
- `tailscale serve` and `funnel` (owned by `setup_tailscale`);
- using an exit node and accepting routes;
- Tailscale in sandboxes or containers;
- more than one host per server entry;
- any agent-initiated change to the node.

Each of these needs its own design change.
