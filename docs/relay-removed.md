# The rendezvous relay was removed

On 2026-10-20 the owner decided to remove the Cloud Run rendezvous relay
from grain completely. That removed:

- the relay server, reverse-tunnel client, authentication, and stored
  settings (`pkg/relay`, `pkg/relaysettings`, `pkg/tunnel/via`,
  `pkg/tunnel/origin`, `cmd/grain-relay`, `Dockerfile.relay`);
- `grain relay` (`pkg/cli/relay.go`, `pkg/cli/relay_admin.go`) and the
  daemon's `-relay-*` flags and `GRAIN_RELAY_*` environment variables (a
  daemon still passed one refuses to start and points here; a stored
  `<data-dir>/relay-settings.json` or `relay/token` secret is ignored at
  start-up with one log line, and imports skip relay settings with a note);
- the daemon's relay wiring and `/api/relay-settings` routes
  (`pkg/daemon/relay_settings.go`, `pkg/ui/relay_settings.go`), the UI's
  Relay settings section (`ui/src/components/RelayPanel.jsx`), and its
  Playwright walkthrough;
- the remote executor relay dial path (`-relay-url` on `grain executor`
  and `--relay` / `--relay-url` in `scripts/run-remote-executor.sh`);
- the Terraform relay module and host wiring (`deploy/terraform/relay`,
  `deploy/terraform/gcp/relay.tf`, `deploy/terraform/gcp/relay-daemon.tf`,
  `deploy/terraform/gcp/deploy/push-relay-image.sh`), the
  `cloudrun-relay` bootstrap playbook, and the `relay image` CI job that
  built `Dockerfile.relay`.

Remote executors still connect directly to the daemon over TCP/TLS
(`-executor-addr`) or HTTP upgrade on `/_grain/executor`,
`/api/executors/connect`, and `/api/executors/stream`. On GCP
(`deploy/terraform/gcp`), reach the daemon over
`gcloud compute start-iap-tunnel` (`terraform output tunnel_command`) or
the VM's HTTPS load balancer with IAP (`expose_ui_publicly = true`,
`iap.tf`).

The last commit on `main` that had the relay is
`1a69637e637e4fe76dd66c803094675febe88788`.

## Restoring it

Revert the removal, or bring back the pieces you need from that commit:

```sh
git revert <the removal's merge commit>
# or, piece by piece:
git checkout 1a69637e637e4fe76dd66c803094675febe88788 -- \
  Dockerfile.relay cmd/grain-relay pkg/relay pkg/relaysettings \
  pkg/tunnel/via pkg/tunnel/origin deploy/terraform/relay
```
