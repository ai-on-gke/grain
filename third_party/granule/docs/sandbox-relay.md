# granule relay

Status: implemented (`pkg/relay`, `granule relay`, `sandbox.DialRelay`, `deploy/k8s/granule-relay.yaml`).

granule relay is how a controller outside a Kubernetes cluster reaches sandbox pods' access listeners without `kubectl exec`. It is a companion to the [sandbox access API](sandbox-access-api.md), and carries that API without reading it.

## 1. Topology

Sandbox pods run granule's access server in **listen** mode on a TCP port (`GRANULE_ACCESS_LISTEN=tcp://0.0.0.0:7443`): TLS 1.3, with the per-sandbox token checked on every connection. The connection always runs **towards** the sandbox:

```text
in-cluster controller ------------------- tls://<podIP>:7443 ------------------> sandbox pod
                                                                                (granule-shim, outside the VM)

off-cluster controller --TLS 1.3 + controller token/mTLS--> granule relay --TCP <podIP>:7443--> sandbox pod
   (one long-lived multiplexed session)                     (LoadBalancer)     (one connection per stream)
```

- **Controller in the cluster:** `sandbox.Dial(ctx, "tls://<podIP>:7443", opts)`. No relay.
- **Controller outside the cluster** (for example on a Cloudtop, where GKE cannot dial in): the controller makes one outbound connection to `Service/granule-relay`, and the relay dials the target pod for each stream.
- **Sandboxes in networks nobody can dial into** (other clusters, NAT'd pools) can still dial out to a controller (`GRANULE_ACCESS_DIAL`, see the API doc). That is an option, not the primary path.

## 2. Protocol

1. The controller dials the relay over TLS 1.3 and sends one JSON line, `{"version":"granule-relay/1","token":"<controller token>"}`. The relay checks the token (constant-time) and/or the client certificate (`-client-ca`), answers `{"ok":true}` or a refusal, and the connection becomes a `pkg/guestforward` session.
2. Each stream the controller opens carries `{"target":"<sandbox or pod name>"}` as its metadata. The relay:
   - checks the name is a plain Kubernetes object name, so it cannot address another namespace or API path;
   - resolves it from its pod cache: a pod in `-namespace` matching `-selector`, by pod name or by the `-name-label` label's value, Running and Ready, with an IP;
   - dials `<podIP>:<-sandbox-port>`, confirms the stream, and splices bytes both ways with `io.Copy`, passing half-closes through.

   A name it will not route to is rejected with the reason, which the client returns as `sandbox.ErrTargetRefused`.
3. Over the spliced stream the controller runs TLS to the sandbox and the ordinary access-API handshake, carrying the per-sandbox token. The relay sees only ciphertext.

The relay is stateless: no disk and no database. It does not parse `exec`, `files` or `forwards` traffic, and it holds no sandbox tokens. Restarting it costs controllers a reconnect. In-flight commands keep running in the sandbox and are resumed with `Reattach`.

## 3. Resolving targets

`relay.PodResolver` is a small informer. It makes one `list` of pods in the namespace with the label selector, then a `watch` from that resourceVersion, relisting when the watch ends. A name missing from the cache, such as a pod created a moment ago, gets a single `get`. The cache entry is checked against the selector again on the relay side. The relay's ServiceAccount needs only `get`, `list` and `watch` on `pods` in that namespace. It never uses `exec`.

## 4. Client

```go
rc, err := sandbox.DialRelay(ctx, "relay.example.com:443", sandbox.RelayCredentials{
    Token:     controllerToken,                       // or TLSConfig.Certificates for mTLS
    TLSConfig: &tls.Config{RootCAs: relayCA},
})
cli, err := rc.Client(ctx, "sandbox-42", sandbox.DialOptions{
    Token:     sandboxToken,                          // checked by the sandbox, end to end
    TLSConfig: &tls.Config{RootCAs: sandboxCA},       // ServerName defaults to "sandbox-42"
})
code, err := cli.Exec(ctx, sandbox.ExecRequest{Argv: []string{"make", "test"}}, nil, stdout, stderr)
```

`DialRelay` makes the first connection before it returns, so a bad controller credential surfaces there as `ErrAuthFailed`. After that it reconnects with exponential backoff whenever the session drops. A `Client` obtained from it redials through the relay on its next operation, so a `Wait` that returned `ErrTransportCut` is followed by `cli.Reattach(ctx, id, offset, ...)` on the same `Client`.

`sandbox.Connector` is the one interface grain and grit use for either transport: `sandbox.RelayConnector{Relay: rc, Options: ...}` off-cluster, and `sandbox.DirectConnector{Resolve: ..., Options: ...}` in-cluster. Both return the same `*sandbox.Client`.

## 5. Running it

```text
granule relay -controller-listen=:8443 \
  -tls-cert=/etc/granule-relay/tls/tls.crt -tls-key=/etc/granule-relay/tls/tls.key \
  -token-file=/etc/granule-relay/token/token [-client-ca=ca.crt] \
  -namespace=granule-sandboxes -selector=granule.gke.io/sandbox=true \
  -name-label=granule.gke.io/sandbox-name -sandbox-port=7443
```

The relay refuses to start without a server certificate, without a selector, or without controller authentication: it needs a token, or `-client-ca`, which makes the client certificate mandatory.

`deploy/k8s/granule-relay.yaml` is a reference manifest containing:

- the ServiceAccount and a Role limited to `get/list/watch pods`;
- the Deployment;
- the `LoadBalancer` Service (restrict `loadBalancerSourceRanges` to the controller).

`deploy/k8s/sandbox-access-networkpolicy.yaml` applies deny-by-default ingress and egress to sandbox pods. Its only ingress allows TCP 7443 from the relay pods and the in-cluster controller's pods, so sandbox-to-sandbox traffic is blocked. It also confines the relay's own egress to sandbox pods' access port and the API server.

## 6. Security summary

| Threat | Defence |
|---|---|
| Unauthenticated party opens a relay session | TLS 1.3, plus the controller token (constant-time compare) and/or a verified client certificate. The relay refuses to start with neither. |
| Relay used to reach arbitrary pods | Targets must be plain names, resolved only in one namespace and only among pods matching the selector, checked again on the relay. Only the configured sandbox port is dialed. |
| Another sandbox, or anything else in the cluster, connects to a sandbox's listener | The NetworkPolicy admits only the relay and controller. The listener also requires TLS and the per-sandbox token on every connection, refusing everything if started without one. |
| Compromised relay | It holds no sandbox tokens or keys. TLS to the sandbox runs end to end, so it can drop or delay traffic but cannot read or forge it. |
| Guest workload reaches the listener or reads the token | The listener and token live in `granule-shim`, outside the VM. With inline networking, netshim keeps TCP to the access port in the pod's stack (`NETSHIM_ACCESS_PORTS`), so it never reaches the guest. |
