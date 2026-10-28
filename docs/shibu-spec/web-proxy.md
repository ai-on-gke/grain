# Shibu web proxy and identity, v1

*Kernel.* Every request from a VM to anything, and every request from
outside Shibu, goes through the web proxy
([design 1](../shibu.md#1-motivation)). This document specifies how it
addresses endpoints, how it knows who is calling, how it enforces
grants on each request, the caller assertion it stamps, tunnels,
egress through `/sys/egress`, public exposure through `/sys/ingress`,
and pane origins.

## Contents

1. [Addressing](#addressing)
2. [Identity](#identity)
3. [The front door](#the-front-door)
4. [Enforcing a grant on a request](#enforcing-a-grant-on-a-request)
5. [The caller assertion](#the-caller-assertion)
6. [Tunnels](#tunnels)
7. [Egress](#egress)
8. [Public exposure](#public-exposure)
9. [Pane origins](#pane-origins)
10. [Responses the proxy itself makes](#responses-the-proxy-itself-makes)

---

## Addressing

| From | Control API | An endpoint by absolute path | An endpoint in the caller's own namespace |
| --- | --- | --- | --- |
| inside a VM | `http://shibu.internal/_shibu/v1/<op>` | `http://shibu.internal/<path>/<rest>` | `http://shibu.internal/_self/<name>/<rest>` |
| outside, through the front door | `https://<shibu>/_shibu/v1/<op>` | `https://<shibu>/<path>/<rest>` | none: a front-door caller names paths in full |

- **Resolution.** The proxy finds the longest prefix of the URL path
  that names an endpoint the caller can see: one it owns by the tree, a
  handle it holds, or a handle visible to it through `subtree: true`.
  What follows that prefix is `<rest>`, and it is forwarded to the
  endpoint as the request path. `http://shibu.internal/factory/svc/git/acme/app.git/info/refs`
  reaches the endpoint `/factory/svc/git` with path
  `/acme/app.git/info/refs`.
- **Handles.** A request may name either the object
  (`/factory/svc/db`) or the caller's handle on it (`/factory/lead/db`).
  The proxy applies the grant either way. Naming the object is allowed
  only when the caller holds exactly one grant that reaches it, or
  reaches it by the tree; otherwise the request must name a handle,
  so that the grant is never ambiguous.
- **Folders.** A grant on a namespace used as a folder makes every
  endpoint in it reachable under the handle:
  `http://shibu.internal/factory/ci/images/ci-base` through a handle
  `images` on `/factory/svc/images`.
- **Volumes** are not reached over HTTP; they are attached to VMs. A
  `volume` endpoint in a URL answers `405`.
- **`_self`.** `/_self/<name>` is the caller's own namespace. An agent's
  harness reaches its model at `http://shibu.internal/_self/model/…`,
  which is what the design writes as `shibu.internal/model`
  ([`agents.md`](agents.md#placements)).
- **Reserved segments.** Segments starting with `_` are the proxy's.
  Besides `_shibu` and `_self`: `/_shibu/version`, `/_shibu/v1/keys`
  ([The caller assertion](#the-caller-assertion)), and the front door's
  own paths under `/_shibu/auth/`.

Beyond v1, every endpoint also gets a DNS name inside VMs, reversing its
path under `shibu.internal`
([design 2.4](../shibu.md#24-beyond-v1-native-port-endpoints)). v1 uses
that naming only for the authority of a `CONNECT` tunnel
([Tunnels](#tunnels)).

---

## Identity

**Ambient identity by origin.** Nothing is handed into a VM for its
identity ([design, Identity](../shibu.md#identity)).

- Every VM has its own network interface, provided by its backend
  driver, with an **unspoofable origin**
  ([`driver/v1`](extension-points.md#backend-driver-contract-driverv1)).
  The driver tells the kernel each VM's origin before the VM can send.
- The proxy keeps a map from origin to VM, and from VM to namespace. A
  request's caller is the namespace of the VM it came from. The map
  changes when a VM is created, deleted, or re-homed (a `move`, or an
  [adopt](kernel-api.md#adopt), which is how a warm pool hands out a
  VM). Nothing in the VM changes, and no credential is injected.
- A request from a VM carries nothing that identifies it. Any
  `Authorization` header it sends is passed to the endpoint untouched
  and means nothing to Shibu.
- The proxy strips every incoming header whose name starts with
  `Shibu-` (case-insensitively), except `Shibu-Session-Binding` from
  the origin of the runner VM the binding was issued to, below.

**Runner sessions.** Every harness runs in a session of a runner, its
agent's `runs_in` ([design, Runners](../shibu.md#runners)). Its
identity depends on where the runner is
([design, Identity](../shibu.md#identity)):

- **A runner on a VM in the agent's own namespace** gets no binding.
  The session's requests carry that VM's origin and are the agent's
  namespace's, like any process in the VM.
- **Any other runner**, a pool runner such as `/sys/runner` or one a
  team runs, acts **as** the agent's namespace through a **session
  binding**. When a runner launches a session for an agent whose `runs_in`
  it serves, it asks the kernel for one (`POST <agent>/agent/bind`), and
  the kernel issues an opaque binding only while the agent's namespace
  holds a live launch grant on that runner
  ([`agents.md`, The agent object](agents.md#the-agent-object)), naming
  one agent, one session, and the runner endpoint and VMs it was issued
  to.
- A request that carries `Shibu-Session-Binding: <binding>` is treated
  as the agent's namespace only if it comes from the origin of a VM of
  the runner the binding was issued to, and the binding names a live
  session. From any other origin the header is stripped, and the
  request is the sender's own. The proxy relies on nothing the runner
  does to make this hold.
- The runner adds the header in its own forwarder, outside the
  session's container (or microVM or VM), so a CLI never holds it. The binding
  can't be minted, listed, granted or passed to another runner, and
  stops working when the session ends or its launch grant ends.
- The caller assertion for such a request says `via: "runner"`, and
  names the session and the runner
  ([The caller assertion](#the-caller-assertion)). A service sees the
  agent's namespace either way; `via` tells it a runner outside that
  namespace carried the call.

**Tool calls through `mcp`.** A session's call to a VM's `mcp` endpoint
is made as the agent (by origin or binding), and checked against the
agent's grant on that endpoint. A command the VM runs for an `exec`
then calls out with **the VM's own origin**, so its traffic is the VM's
home namespace's, not the agent's
([`agents.md`, VM `mcp`](agents.md#vm-mcp-contract-mcpv1)). The proxy
maps each `mcp` tool call to its op (`read`, `write`, `exec`) and
refuses calls the grant doesn't cover before forwarding.

**Callers with no VM** authenticate at the front door, below.

---

## The front door

*Kernel.* The front door authenticates people and other callers with
no VM, and maps each to the namespaces it may act as
([design, Identity](../shibu.md#identity),
[2.2.18](../shibu.md#2218-human-as-a-principal)).

- **Authentication** is delegated: an OIDC provider, or an identity
  header from IAP or a similar proxy in front of Shibu. The front door
  keeps an **identity list**, like grain's `pkg/access`: each entry maps
  an identity (an email, a group, a service account) to a person name
  and the namespaces it may act as. A person may act as
  `/users/<name>`, and as `/` if they are a root operator.
- **Browser sessions** use a host-only cookie,
  `__Host-shibu-session`, `Secure`, `HttpOnly`, `SameSite=Lax`. It
  never reaches a pane origin ([Pane origins](#pane-origins)).
- **CLI sessions.** `shibu login` runs the OAuth device flow against
  `/_shibu/auth/device`, and stores the session credential it gets back
  in the CLI's config. This credential authenticates a person to the
  front door, exactly as the cookie does. It is not a Shibu token: it
  grants nothing, can't be narrowed, and is useless from inside a VM,
  where the proxy ignores it.
- **Acting as a namespace.** A request names the namespace it acts as
  in `Shibu-Act-As: <path>`. Without it, the front door uses
  `/users/<name>`. A namespace the identity may not act as gets
  `permission_denied`. Acting as `/` is an audit line
  (`front_door.act_as`). The header is honoured only at the front door.
- **What it forwards.** The front door turns the request into the same
  thing a VM's request is: a call made as one namespace, with a caller
  assertion whose `vm_id` is `null`.
- **Who the person is.** The assertion names the person as well as the
  namespace: the identity provider and subject the front door
  authenticated, in its `person` claim
  ([The caller assertion](#the-caller-assertion),
  [design 6.9](../shibu.md#69-callers-with-no-vm)).

`GET /_shibu/v1/whoami` returns
`{"person": "alice", "acting_as": "/users/alice", "may_act_as": ["/users/alice", "/"]}`
to a front-door caller, and `{"ns_path": …, "vm_id": …}` to a VM.

---

## Enforcing a grant on a request

For a request to an `http` endpoint, the proxy:

1. **Identifies** the caller ([Identity](#identity)).
2. **Resolves** the endpoint ([Addressing](#addressing)), or answers
   `404` if the caller can't see it.
3. **Finds the authority**: the tree, if the endpoint is in the
   caller's subtree; otherwise the grant behind the handle.
4. **Checks the grant**, if there is one:
   - the method is in `ops`, directly or through `read` or `write`;
   - `<rest>` matches one of `limits.paths`, if any;
   - an upgrade (WebSocket or `CONNECT`) needs `tunnel` in `ops`, and
     a free slot under `max_connections`;
   - every grant up the chain is still active and allows the same.
   A failure answers `403` with code `permission_denied`.
5. **Stamps** the caller assertion, and forwards.

There is no separate egress step: a request to a Shibu endpoint is
allowed exactly when the tree or a grant allows it, and a request to an
outside host is a request on `/sys/egress`, checked the same way
([Egress](#egress)).

Requests with no grant (by the tree) get every op the endpoint has. A
grant on a contract endpoint may carry ops and limits the proxy doesn't
know, such as `exec`, a model budget or a git ref pattern. For those the
proxy checks only `paths`, passes the ops and limits on in the
assertion, and the endpoint enforces them
([`kernel-api.md`, Ops and limits](kernel-api.md#ops-and-limits)).

**Revocation.** In-flight HTTP requests through a grant that ends are
allowed to finish. New requests are refused at once. Tunnels are not
requests: they are closed at once ([Tunnels](#tunnels)).

---

## The caller assertion

*Kernel* ([design 2.2.22](../shibu.md#2222-caller-assertion)).

The proxy stamps every request it forwards with a signed, short-lived
assertion of who is calling, in the `Shibu-Caller` header.

**Format.** A JWS in compact serialisation, signed with ES256. Its
claims:

```json
{
  "iss": "https://shibu.example.com",
  "aud": "ep_51x",
  "iat": 1791696600,
  "exp": 1791696660,
  "jti": "a1b2c3",
  "endpoint": {"id": "ep_51x", "path": "/factory/svc/db"},
  "ns_id": "ns_w17",
  "ns_path": "/factory/lead/workers/w17",
  "vm_id": "vm_17a",
  "grant": {"id": "gr_g", "ops": ["tunnel"], "limits": {"max_connections": 4, "max_duration": "8h"},
            "chain": ["ep_51x", "gr_77", "gr_g"]},
  "via": null,
  "session_id": null,
  "kernel": false,
  "person": null
}
```

| Claim | Meaning |
| --- | --- |
| `aud` | The stable ID of the endpoint the request was forwarded to. A service must check it, so an assertion sent to one service can't be replayed to another. |
| `exp` | At most 60 seconds after `iat`. |
| `ns_id`, `ns_path` | The namespace the call is made as. For a request that came in from outside through `/sys/ingress`, that is `/sys/ingress`'s namespace, with `via: "ingress"` ([Public exposure](#public-exposure)). |
| `vm_id` | The calling VM, or `null` for a front-door caller and for the kernel. |
| `grant` | The grant the request came through, with its effective `ops` and `limits` (each narrowed by every link of its chain) and its chain. `null` when the caller reaches the endpoint by the tree. |
| `via` | `"runner"` when a harness session made the call as the agent through a session binding ([Identity](#identity)); `"ingress"` when `/sys/ingress` forwarded an outside request, in which case it also adds `ingress: {"route", "audience_match", "verified_by"?}`; otherwise `null`. |
| `session_id`, `runner` | With `via: "runner"`, the harness session and the runner endpoint carrying it. |
| `kernel` | `true` when the kernel itself is calling the endpoint: a component allocator's `create` or `delete`, a conformance run. `ns_path` is then `/`. |
| `person` | Who is behind a caller with no VM ([design 6.9](../shibu.md#69-callers-with-no-vm)). For a front-door caller, `{"idp": "<issuer>", "sub": "<subject>", "name": "<person name>"}`: the identity provider and subject the front door authenticated, and the person name its identity list maps them to; `ns_id` and `ns_path` still name the namespace acted as. For an anonymous caller through `/sys/ingress`, `{"anonymous": true}`, with `ns_path` `/sys/ingress` and `via: "ingress"`. `null` for a VM, a harness session and the kernel. |

**Keys.** The verification keys are a JWKS at
`/_shibu/v1/keys`, served both inside VMs and at the front door, so a
service can verify an assertion even when it isn't directly behind the
proxy. Keys are published at least an hour before the proxy signs with
them, and stay published at least an hour after it stops.

**What a service does with it.** It verifies the signature, `iss`,
`aud` and `exp`, then applies its own policy to the rest: a git service
letting only `/factory/mq` push to `main`, `/sys/model` charging the
grant named in `grant.id`. A service that isn't behind the proxy at all
must not trust an unsigned identity.

**Per transport** ([design 2.2.22](../shibu.md#2222-caller-assertion)):

- **HTTP:** on every request.
- **Tunnels:** once, on the upgrade request that opens the tunnel. Its
  60-second lifetime doesn't bound the tunnel; the proxy closes the
  tunnel when the grant ends. `shibu serve` can pass it on to the local
  service in a PROXY protocol v2 header ([Tunnels](#tunnels)).
- **Native TCP**, beyond v1: mTLS or PROXY protocol v2
  ([design 2.4](../shibu.md#24-beyond-v1-native-port-endpoints)).

---

## Tunnels

*Kernel: upgrades, the `tunnel` op, and closing on revoke. Standard
distribution, optional: the shim*
([design 2.2.26](../shibu.md#2226-non-http-services-tunnels-over-http-endpoints)).

**Opening a tunnel.** On any `http` endpoint, a caller holding
`tunnel` may open a tunnel in one of two forms:

- **WebSocket** (the shim's default): an ordinary
  `GET <endpoint>/<rest>` with `Upgrade: websocket` and the subprotocol
  `shibu-tunnel.v1`. Once upgraded, each binary message carries raw
  bytes, in either direction. Text messages are not used.
- **`CONNECT`**, for clients that speak it already (an SSH
  `ProxyCommand`, `curl --proxytunnel`): `CONNECT` to the proxy at
  `shibu.internal:3128`, with the authority
  `<endpoint path, segments reversed and joined by dots>.shibu.internal:<port>`,
  for example `CONNECT db.svc.factory.shibu.internal:5432`. The port is
  ignored: the endpoint names the target.

The grant check and the caller assertion happen on the upgrade request
([Enforcing a grant](#enforcing-a-grant-on-a-request)). Nothing is
checked per byte after that.

**On the server side**, the proxy always opens the tunnel to the
endpoint as a WebSocket upgrade with subprotocol `shibu-tunnel.v1`, the
caller assertion on the upgrade request, and `<rest>` as its path
(`/` by default), whichever form the client used. A server needs to
handle only one form; `shibu serve` does.

**Limits** on a `tunnel` grant:

| Limit | Enforced as |
| --- | --- |
| `max_connections` | open tunnels through the grant at once, across all of the grantee's VMs |
| `bandwidth` | bytes per second through the grant, both directions together |
| `max_duration` | how long each tunnel may stay open |

**Closing.** When the grant ends (revoke, lease, cascade, or the
validity rule), the proxy closes **every** tunnel opened through it, at
once ([design, Grants](../shibu.md#grants)). A WebSocket is closed with
code `4403` and reason `grant ended`; a `CONNECT` tunnel's TCP
connection is closed. `max_duration` closes with `4408`.

### The shim

*Standard distribution, optional.* Shipped in the base images as the
`shibu` command
([design 2.2.26](../shibu.md#2226-non-http-services-tunnels-over-http-endpoints)).

```
shibu forward <endpoint> <local-port> [--connect]
shibu serve   <endpoint> --to <host:port> [--listen <port>] [--proxy-protocol]
```

- `shibu forward` listens on `localhost:<local-port>` and opens one
  tunnel per accepted connection, by WebSocket unless `--connect`.
- `shibu serve` listens on the port the endpoint points at
  (`--listen`, or read from the endpoint), accepts `shibu-tunnel.v1`
  upgrades from the proxy, and forwards the bytes to `--to`. It
  verifies each upgrade's caller assertion. With `--proxy-protocol` it
  sends the local service a PROXY protocol v2 header first, with the
  compact assertion in TLV type `0xE0`.
- Both are ordinary clients of this section. Any other implementation
  of the same wire form works the same.

---

## Egress

*Kernel: no route out but the proxy. Standard distribution: `/sys/egress`*
([design 2.2.13](../shibu.md#2213-egress-policy)).

- **No direct route.** Every packet a VM sends to anything other than
  `shibu.internal` goes to the driver's egress enforcement point, which
  drops it
  ([`driver/v1`](extension-points.md#backend-driver-contract-driverv1)).
  The only VMs with a route out are `/sys/egress`'s own, which its
  `/sys` spec entry marks `network: outside`
  ([`sys-spec.md`](sys-spec.md#components)).
- **Always reachable.** `shibu.internal`: the control API, the VM's own
  namespace's endpoints, and the endpoints its namespace holds grants
  on. Nothing else needs a grant on `/sys/egress`.
- **Outside hosts go through `/sys/egress`.** The kernel exports
  `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` (`shibu.internal`) in
  every VM, pointing at `http://shibu.internal:3128`. A forward-proxy
  request or a `CONNECT` there for an outside host is a request on the
  `/sys/egress` endpoint, made through whichever handle on it the
  caller's namespace holds (by the tree, a grant, or a grant to an
  ancestor with `subtree: true`; with several, any one that allows it).
  The proxy checks it as any request ([Enforcing a
  grant](#enforcing-a-grant-on-a-request)): `tunnel` for a `CONNECT`,
  `read` or `write` for plain HTTP. `/sys/egress` checks the
  destination against the grant's `hosts` from the caller assertion,
  and connects ([`egress/v1`](extension-points.md#egressv1)).
- **No grant, no outside access.** A namespace holding no handle on
  `/sys/egress` gets `403` with code `egress_denied` from the proxy for
  any outside host; a destination outside `hosts` gets the same from
  `/sys/egress`. Each refusal is an audit line.
- **What `exec` lends.** A command run through a VM's `mcp` `exec`
  leaves with that VM's origin, so it uses that VM's namespace's
  `/sys/egress` grant, not the caller's
  ([`agents.md`, `mcp/v1`](agents.md#vm-mcp-contract-mcpv1)).

---

## Public exposure

*Standard distribution: `/sys/ingress`*
([design 2.2.12](../shibu.md#2212-pre-approved-publish)).

Publishing an `http` endpoint is **granting it to `/sys/ingress`**
([`ingress/v1`](extension-points.md#ingressv1)). There is no public
endpoint object and no allocator for one.

- **Serving.** `/sys/ingress` serves each endpoint granted to it on
  its own host, at `https://ingress.<shibu>/<handle name>`: for
  `grant(/factory/lead/vm-hook, to=/sys/ingress, ops=[POST], as=factory-lead-webhook)`,
  `https://ingress.<shibu>/factory-lead-webhook`. The front door
  doesn't serve that host; the front door's cookies never reach it.
- **Who may reach it.** `/sys/ingress` decides, from the grant's labels:
  `ingress.shibu.dev/audience` is `anyone`, or a comma-separated list
  of people or groups it checks with an identity-aware sign-in of its
  own; without the label the route answers no one. An
  `ingress.shibu.dev/verifier` label names an `ingress-verifier`
  endpoint of `/sys/cred` to call first
  ([`credential-proxy/v1`](extension-points.md#credential-proxyv1));
  `/sys/ingress` needs a grant on it too. A deny answers `401`, and
  nothing reaches the endpoint.
- **Forwarding.** An allowed request is forwarded through
  `/sys/ingress`'s grant, so the grant's ops and `paths` bound what the
  outside can do, and the endpoint sees a caller assertion with
  `ns_path` `/sys/ingress`, `via: "ingress"`, and the route and
  audience match. `/sys/ingress` handles anonymous callers entirely:
  for a route whose audience is `anyone`, the assertion marks the
  caller anonymous (`person: {"anonymous": true}`), and nothing about
  the outside caller reaches the endpoint but the request itself
  ([design 6.9](../shibu.md#69-callers-with-no-vm)).
- **Unpublishing** is revoking the grant, or its lease running out. The
  URL stops answering at once.

---

## Pane origins

*Kernel: the origins and their sessions. The pane convention is in*
[`ui-conventions.md`](ui-conventions.md#agent-served-panes)
([design 2.6.6](../shibu.md#266-agent-served-panes)).

- **One origin per namespace**, by stable ID:
  `https://<ns-id>.panes.<shibu>`. A namespace re-created at the same
  path gets a new ID and so a new origin, with no browser storage of
  the old one.
- **Only `ui/` endpoints.** `https://<ns-id>.panes.<shibu>/<id>/<rest>`
  is `<ns>/ui/<id>/<rest>`. Any other path on a pane origin answers
  `404`, so a pane origin can't be used to reach the namespace's other
  endpoints, or anyone else's.
- **Its own session.** The UI's session cookie is host-only and never
  reaches a pane origin. The front door gives each pane origin its own
  session, scoped to that origin and to that namespace's `ui/`
  endpoints: the pane's first load redirects through
  `/_shibu/auth/pane?origin=<ns-id>`, which sets `__Host-shibu-pane`
  on the pane origin (`Secure`, `HttpOnly`, `SameSite=None`,
  `Partitioned`). A request on the pane origin is made as the viewer,
  so a pane's own fetches see only what the viewer could reach anyway.
- **Framing.** Every response on a pane origin carries
  `Content-Security-Policy: frame-ancestors <the UI's origin>`. The
  UI's own origin answers with `frame-ancestors 'none'`.
- **No credential entry.** The front door never serves a sign-in or
  credential form on a `*.panes.` origin.

---

## Responses the proxy itself makes

When the proxy answers instead of the endpoint, the body has the error
shape in [`README.md`](README.md#errors).

| Status | `code` | When |
| --- | --- | --- |
| `404` | `not_found` | No endpoint the caller can see matches the path. The same answer whether it exists or not. |
| `403` | `permission_denied` | The grant doesn't allow the method, the path, or a tunnel. |
| `403` | `egress_denied` | A request for an outside host from a namespace that holds no handle on `/sys/egress` ([Egress](#egress)). |
| `405` | `unsupported` | The endpoint isn't one HTTP can reach (a volume, a secret). |
| `409` | `quota_exceeded` | A tunnel over `max_connections`. |
| `502` | `component_unavailable` | The endpoint's VM didn't answer, or isn't running. |
| `504` | `component_unavailable` | The endpoint didn't answer in time. |

An endpoint's own answers, including a metered endpoint's `429`
`budget_exhausted`, pass through unchanged.
