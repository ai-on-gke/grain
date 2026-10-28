# 2026-10-22: run locations with no host executor (task 5031)

What was run to verify that "host" is refused or hidden where a
deployment has no host executor
([docs/design/cli-run-location.md](../../design/cli-run-location.md),
"Deployments with no host executor"). Nothing was deployed to a real
cluster. The Kubernetes behaviour is checked against the manifests in
`deploy/kubernetes` and against `grain demo`.

## Go

- `go test ./pkg/daemon -run 'HostExecutor|Hostd|NoHost|BareMachine|KubernetesHasNo'`
  checks resolution on Kubernetes, in a container with no hostd, and with
  hostd configured but down. The "down" case dials a socket path nothing
  listens on, and the run's error names it.
- `go test ./tests/security -run CLIRunSettingsRefuseHostWhereThereIsNoHost`
  sends `PUT /api/settings/cli-run` with `host` for every CLI, as each
  no-host daemon serves it. Each gets a 400, and nothing is stored.
- `go test ./tests/deploy -run TestTheKubernetesDaemonsStartWithDefaultRunLocationsAndNoHostd`
  covers both `grain.yaml` and `grain-granule.yaml`.

## Settings → Models, against `grain demo`

`grain demo -host-executor=kubernetes` resolves Settings as
`deploy/kubernetes/grain.yaml`'s daemon would. No stubs:
`ui/e2e/cli-run-locations-kubernetes.spec.js` (Playwright, Chromium)
drives the real server. It checks that:

- the notice says why there is no Host;
- every CLI's select offers only Default, Auto and Container;
- a `PUT` of `host` returns 400;
- Container saves;
- the notice sits above the rows inside the card, with no horizontal
  overflow, in light and dark.

`ui/e2e/cli-run-locations.spec.js` (the bare-machine demo) still passes
unchanged.

Screenshots were taken with `node e2e/screenshot.mjs --url …
--pages '/settings/models#cli-run'`:

- `kubernetes-*`: Kubernetes mode at desktop (1440) and phone widths, in
  light and dark. `contact-sheet.png` puts the four together.
- `hostd-down-*`: `grain demo -host-executor=hostd-down` at desktop, in
  light and dark. This is a container daemon whose hostd is configured at
  a socket nothing listens on, with `-agy-run=host`. The card shows the
  warning that hostd is not answering, and agy stays on
  "host (through grain hostd)".
