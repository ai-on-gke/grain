# The "none" sandbox backend: a control-plane-only daemon

`none` (`model.SandboxBackendNone`, selected with
`grain daemon -disable-native-sandboxes`, `Config.DisableNativeSandboxes`)
is the sandbox backend that creates no sandboxes at all. It used to be
the backend for a deployment whose tasks all ran on remote executors.
Remote executors were removed in task 2324 (see
[executors-removed.md](executors-removed.md)), so on its own the backend
now runs no task anywhere.

It was kept rather than retired because it still has a use: **a daemon
that runs grain's control plane and nothing else.** That covers the UI,
Settings, the inbox and task list, admission and holds, the config
repository and state-repository sync, schedules, and CI and merge-queue
bookkeeping. Every other backend needs host plumbing (Docker, granule,
a cluster, a sandbox directory), and the daemon refuses to start without
one (`no sandbox backend selected`). `none` is how you start a real
daemon without any of that:

- Playwright specs that start their own daemon, such as
  `ui/e2e/host-sandboxes-alert.spec.js` and
  `ui/e2e/k8s-sandbox-settings-walkthrough.spec.js`.
- The container test harness (`tests/container/harness_test.go`).
- Live walkthroughs recorded under `docs/test-runs/`, which start
  `grain daemon -data-dir … -sandbox-port-base=0 -disable-native-sandboxes`
  to exercise settings, access and config-repo behaviour in a task
  sandbox that has no Docker or VM support.

Do not use it for a deployment that is meant to do work.

## What dispatch does

Admission still runs, so holds, approvals and "dispatched on the next
cycle" can all be observed. Each dispatch then fails when it acquires
its sandbox (`orchestrator.DisabledSandboxes`), and the run records
`orchestrator.ErrNoSandboxBackend` as its reason:

> this deployment's sandbox backend is "none" (-disable-native-sandboxes):
> it runs grain's control plane only and creates no sandboxes, so no task
> can run here. Choose a sandbox backend in Settings -> Sandboxes, or
> restart the daemon with -container-sandboxes, -granule-sandboxes,
> -k8s-sandboxes, -k8s-granule-sandboxes or -host-sandboxes

At startup the daemon logs
`grain daemon: sandbox backend "none" -- this daemon runs the control
plane only and creates no sandboxes, so no task will run`.

## In Settings

Settings → Sandboxes lists the backend as **None (control plane only)**.
It can be chosen only on a daemon started with `-disable-native-sandboxes`
("needs -disable-native-sandboxes at daemon startup" otherwise). As with
every backend change, it applies at the next restart. A daemon started
with the flag keeps `none` among its choices after it has been switched
to another backend, so it can be switched back.
