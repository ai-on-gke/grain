# Host Tailscale capability (install and configure)

**Status:** proposed. Normative detail is in [`design/spec/tailscale/tailscale.spec.md`](../spec/tailscale/tailscale.spec.md); requirement IDs below (`TS-n`) point there.

Grain already puts its host on a tailnet: `scripts/setup.sh`'s `setup_tailscale` (`GRAIN_TAILSCALE_ENABLE=1`) installs Tailscale on the host, runs `tailscale up` with an auth key from the environment, and points `tailscale serve` at the UI, which `pkg/ui/hostguard.go` accepts under its `*.ts.net` name. That only happens at install time, from a root shell, and it takes free-form `GRAIN_TAILSCALE_UP_ARGS`. This design adds a capability server, `grain capability tailscale`, that lets a person install, configure and observe that same node from Settings, and lets an agent *read* it, inside the capability model of [`docs/capabilities.md`](../../docs/capabilities.md) and [`capabilities.spec.md`](../spec/capabilities/capabilities.spec.md).

## Goals

- **Install** a pinned Tailscale version on a Debian host from `pkgs.tailscale.com/stable`, with the repository signing key checked against a pinned fingerprint and the package held at that version (TS-20..TS-27).
- **Configure** the node (`tailscale up` / `tailscale set`) from a closed set of options: hostname, tags, `--ssh`, exit-node advertisement and subnet-route advertisement (TS-30..TS-39).
- **Observe:** `tailscale status` as structured, redacted data, for a person in Settings and for an agent through a grant (TS-50..TS-56).
- **Teardown:** a release that never touches the node, and separate, explicit `down` and `logout` operations for a person (TS-60..TS-64).
- Every operation audited with its caller (TS-70..TS-74).

## Non-goals

- Free-form `tailscale` arguments, from anyone grain hands them to (TS-31).
- `tailscale serve`, `funnel`, `cert`, `--accept-routes`, using an exit node (`--exit-node`), `--shields-up`, `--operator`, `--reset`. The first three belong to `setup_tailscale` and the UI. The rest change the host's own routing, which sandboxes and the proxy cache depend on (TS-32).
- Uninstalling Tailscale, managing tailnet ACLs, or creating auth keys through Tailscale's API.
- Non-Debian hosts, hosts without systemd, and Tailscale inside a sandbox or the daemon's container. A sandbox can still `apt-get install tailscale` through the proxy cache, but that has nothing to do with this capability.
- More than one host per server entry. The host is the one whose grain hostd serves the entry.

## Trust model

A node on the tailnet can reach, and be reached by, everything the tailnet ACL allows, from the host's network. `--ssh` hands host logins to the tailnet, and an exit node or subnet route hands the host's network to other nodes. Each of these is a decision about the operator's network, so only a person makes it, and they make it explicitly.

Three layers, each narrower than the one before:

1. **Host policy, `/etc/grain/tailscale.yaml`** (root-owned, like hostd's allowlist). It is the ceiling: the allowed versions, the signing-key fingerprint, the tags, whether SSH, exit-node and routes can be turned on at all, and whether peers may be shown. Nothing grain stores can widen it, because grain cannot write `/etc/grain` (TS-10..TS-14).
2. **Server configuration (`configure`)**, edited in Settings by a person. It is the desired state inside that ceiling: the version, the hostname, the tags, the toggles, and the auth key as a `$secret:` (TS-15..TS-19).
3. **Operations**, run by a person, never by an agent, from Settings: `plan`, `install`, `apply`, `down`, `logout`. A change that widens exposure must be acknowledged item by item in the request (TS-36).

**Privilege split.** hostd runs its entries as the operator's uid, and installing a package or running `tailscale up` needs root. The server therefore never runs `tailscale` or `apt-get` itself. It talks to a small root helper, `grain tailscale-helper`, which systemd starts on a socket only the operator's uid can open. The helper re-checks every request against the host policy and runs a fixed set of argv vectors (TS-3..TS-8). We do not use `tailscale set --operator=<user>`: it would give every process running as that uid, host scripts and host-run agent CLIs included, unrestricted control of tailscaled.

**Relation to `setup_tailscale`.** This is the same node and the same tailscaled. The capability adopts a node that is already up as it is and reports any drift without changing anything (TS-35). It never touches `serve` or `funnel` state (TS-32). A hostname change, which changes the UI's `*.ts.net` URL, and a `down` or `logout` while serve is configured, which cuts the UI off the tailnet, each need their own acknowledgement (TS-37, TS-62). `hostguard.go` needs no change. The install writes the same keyring path and source line that `setup.sh` writes, so a re-run of either one leaves the other's work alone (TS-24).

## Decisions

**1. Per-attempt lease or operator-level resource? Operator-level.** Tailscale state outlives every attempt, and logging a node in and out per attempt would churn the tailnet and break the UI. Install, configure and teardown are operator operations run from Settings: `tailscale_*` control-plane tools, the way `host-script`'s `test_script` is one, behind `POST /api/capability-servers/tailscale/<op>` (TS-40..TS-46). MCP `configure` only *validates and records* the desired state, because grain calls it on every attach and restart, and changing the host there would mean a daemon restart could re-login the node (TS-16). The one agent-facing method is `#tailscale-status`, a stateful lease (§6.2 pattern) that serves a read-only `tailscale_status` tool for the attempt (TS-50).
*Trade-off:* an agent cannot fix a broken node on its own. It can diagnose and ask a person. We judged that an agent with a reason to change the node is exactly the case that needs a person.

**2. Who may grant it? Only a person.** `#tailscale-status` is registered with `protocol.RequirePersonGrant`, so no `propose_task` and no parent grant covers it. Its tool is model-only and is never placed on the sandbox CLI credential. The operation routes need `capabilities:admin`, a dangerous scope, and are refused to every agent API caller, even one that holds that scope (TS-51, TS-45). This puts it in the same class as secrets and settings.
*Trade-off:* even read-only status needs a person to grant. Status reveals the node's addresses, tags and (if policy allows) peers, which map the tailnet, so we accept the friction.

**3. How far can an agent steer the node? Not at all; a person, within a closed schema.** No argument pass-through: each option is a typed field that maps to exactly one flag (TS-30, TS-31). SSH, exit-node, routes and tags must each be allowed in host policy, set in configuration, and acknowledged in the `apply` request when they widen exposure (TS-33, TS-36).
*Trade-off:* flags outside the schema (such as `--accept-dns=false` or `--netfilter-mode`) are only reachable through `setup.sh`'s `GRAIN_TAILSCALE_UP_ARGS` or by hand. When `tailscale up` refuses because of such a pref, the capability fails and names it (TS-35). It never adds `--reset`.

**4. Where does the auth key live, and how is it rotated? `$secret:` only.** It is the server's write-only `auth_key` field, resolved in memory, and it reaches the helper in a request body over the private socket. The helper writes it to a root-only tmpfs file for `--auth-key=file:` and deletes it. It never appears in argv (`/proc/*/cmdline` is world-readable), a grant, a log, an error or an audit record (TS-17, TS-38). The key is used only when the node logs in, so rotating it never disrupts a running node: create a new key, paste it into Settings, and revoke the old one in Tailscale's admin console. The node's own key expiry is separate: tagged nodes do not expire by default, and status warns 14 days before expiry otherwise (TS-19, TS-55). We recommend a single-use, pre-authorized, tagged key with a short expiry. We recommend against an ephemeral key, because an ephemeral node disappears from the tailnet whenever it goes offline.
*Trade-off:* a lost key stops nothing until the next login. A login then fails clearly as `needs_auth_key` (TS-81).

**5. Audit.** Each operation is recorded in three places. The helper's journal (`journalctl -u grain-tailscale-helper`) holds what root actually ran. The server's `<data-dir>/audit.jsonl` records every call with its `caller`. Grain's store holds person operations in the credential-access audit log (`model.CredentialAccessEntry`, kind `tailscale`, with the person's identity), and status calls on the attempt's run record through the release audit (spec §4.4.4) (TS-70..TS-74).
*Trade-off:* there are three places to look. Each answers a different question (what root ran, what the server was asked, who asked), and the store copy is the one people read.

**6. Failure modes.** The spec gives a stable error code and a recovery for each (TS-80..TS-88):
- **Node already up:** adopted, never re-logged in. Drift is applied with `tailscale set` where possible, and a tag change requires a re-auth acknowledgement.
- **Lost auth key:** status and running-node changes keep working. A login fails as `needs_auth_key`.
- **Version no longer served:** `version_unavailable`, listing the allowed versions that are still served. The installed version is untouched, and there is never a fallback to latest.
- **No systemd:** the helper cannot exist. The server reports `needs-config` with `unsupported_host`, and there is no userspace-tailscaled fallback.

## Rollout

The implementation is a follow-up task gated on this design and spec. It covers the helper, the server, the hostd allowlist built-in `grain-tailscale`, the systemd units in `setup.sh`, the Settings card, and docs. `setup.sh` may write a minimal host policy (status only, no versions) so that a fresh install offers observation and nothing else until a person widens it.
