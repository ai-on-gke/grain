# Quickstart: a Grain of your own

This takes a fresh workstation or Linux VM to a first finished
task in about fifteen minutes. The Grain it sets up is single-tenant:
it holds one backlog, belongs to the account that runs
it, and needs no sign-in. It runs the tasks you file.

> **Want the mental model first (or next)?** Read
> **[Architecture](../design/design/architecture.design.md)** for a 25-minute end-to-end guide to how
> Grain holds together—the daemon, sandboxes, task states, CI and merge queue,
> where state lives, and where to look when something is stuck.

Nothing here needs root, except installing docker if you want container
sandboxes.

| Step | Time |
|------|------|
| 1. Install the `grain` binary | 2 min |
| 2. Get a sandbox and a model CLI ready | 0 to 5 min |
| 3. `grain init workstation` | 2 min |
| 4. Add a model connection | 2 min |
| 5. File a first task | 3 min |

## 1. Install the binary

Releases are published as the signed container image
`ghcr.io/bwsalmon/grain/grain:<version>`, the core build (`./cmd/grain`). The
wrapper distribution build (a wrapper distribution's `cmd/grain`) is `ghcr.io/bwsalmon/grain/google-grain`,
with the same tags. Its `/usr/local/bin/grain` is a static Linux binary
(`CGO_ENABLED=0`). Use a release tag (`vX.Y.Z`, listed
in [docs/releases](releases)) rather than `latest`, and pin the digest you
verified:

```sh
VERSION=v1.9.0                       # the release you want
IMAGE=ghcr.io/bwsalmon/grain/grain:$VERSION
docker pull "$IMAGE"
PINNED=$(docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE")
echo "$PINNED"                       # ghcr.io/bwsalmon/grain/grain@sha256:...

# Verify the release signature (docs/image-signing.md). cosign.pub is the
# release public key your Grain operator publishes.
cosign verify --key cosign.pub --insecure-ignore-tlog=true "$PINNED"

mkdir -p ~/.local/bin
id=$(docker create "$PINNED")
docker cp "$id:/usr/local/bin/grain" ~/.local/bin/grain
docker rm "$id" >/dev/null
sha256sum ~/.local/bin/grain         # record it beside $PINNED
grain version
```

`scripts/install-grain.sh` does the same in one command, and refuses to
install an image whose signature does not verify when you give it a key:

```sh
scripts/install-grain.sh -version v1.9.0 -key cosign.pub
```

If `~/.local/bin` is not on your `PATH`, add it. Without docker, copy the
binary off any machine that has it. To build from source instead (macOS
included), use `make dist-cli`. It writes `bin/dist/grain-<os>-<arch>.tar.gz`
and a `checksums.txt` to check them against.

## 2. Get a sandbox and a model CLI ready

Every task runs in a sandbox. `grain init workstation` uses the strongest
one this machine can run:

| Backend | Chosen when | Isolation |
|---------|-------------|-----------|
| `granule` | `granule` on `PATH`, `/dev/kvm` usable and docker answering | a VM per run |
| `container` | `docker info` works as you | a container per run |
| `host` | neither | **none**: runs execute as you, with your files, keys and network |

For containers on Debian:

```sh
sudo apt-get install -y docker.io
sudo usermod -aG docker "$USER"      # then log out and back in
docker info >/dev/null && echo ok
```

With `container`, init creates the docker network the sandboxes join
(`grain-sandboxes`) if it is missing. It writes that network's gateway
into `daemon.sh` as `-container-git-proxy-host`, the address a sandbox
reaches the daemon at. To use a different address, pass
`-container-git-proxy-host` to init.

`host` is a real option for a machine you alone use and file every task
on. The policy then runs only your own tasks there, and it can't keep a
run off the network.

The model CLI is the first of `claude`, `codex` and `agy` found on your
`PATH`. Install the one you use. With none, the default is `agy`
(Antigravity, which calls the Gemini API).

## 3. Run `grain init workstation`

```sh
grain init workstation
```

It asks for each choice, with what it detected as the default:

- **Data directory**: `~/.grain` (or `$GRAIN_DATA_DIR`).
- **Repositories** tasks may work on: `owner/name`, a URL, or the path of
  a checkout (its `origin` is read). Nothing else is allowed.
- **Your requester identities**: git's `user.email` and your account
  name. These are `local_requesters` and `approvers` in the policy.
- **Capabilities** tasks may be granted: none unless you name some.
- **Sandbox backend** and **sandbox network** (`open` or `none`).
- **Model backend**.
- **A GitHub token file**. It is stored as the `workstation` credential,
  narrowed to the repositories above, and the daemon makes it the
  deployment's git connection for each of them when it starts. Leave it
  blank and the deployment starts with no git connection: add one later
  in **Settings → Git → Add connection** (the gear at the right-hand end of every page's header) -- a GitHub token or
  GitHub App for github.com, or an ambient connection that borrows this
  host's `gh` (or another registered tool's) login.
- **Enable and start `grain.service`**.

The same in one non-interactive line, as CI runs it:

```sh
grain init workstation -non-interactive \
  -repo acme/widgets -repo ~/src/tools \
  -github-token-file ~/.config/grain/github-token \
  -network open
```

Flags it is not given take the detected default. `grain init workstation
-h` lists them all.

### What it writes

| File | What it is |
|------|------------|
| `~/.grain/grain_policy.yml` | The starter [local policy](local-policy.md). A re-run keeps your edits; `-force` rewrites it. |
| `~/.grain/daemon.sh` | The daemon's whole command line, with the `PATH` init ran under. A re-run rewrites it. |
| `~/.grain/secrets/github/workstation.token` | The GitHub token, when one is given. |
| `~/.config/systemd/user/grain.service` | The systemd user unit that runs `daemon.sh`. |

The starter policy fails closed:

```yaml
repos: ["acme/widgets"]
local_requesters: ["me@example.com", "me"]
approvers: ["me@example.com", "me"]
allow_unapproved_external: false
capabilities:
  allow: ["grain-ci"]  # and the ones you named
network: ["open"]
allow_external_on_host_sandboxes: false
```

A task naming any other repository, granted any capability you did not
name, or asking for another network is refused before it gets a sandbox.
`grain-ci` is the one capability allowed without being named: grain grants
it to every task on a repository whose CI mode is grain, and it carries no
credential -- it is the run's own `wait_for_checks` against the
repository's `.grain/workflows`.
So is one from anybody else that does not carry your approval.
Edit the file, then `systemctl --user restart grain`.

`daemon.sh` runs `grain daemon -policy ... -ui-addr
127.0.0.1:4242 -sandbox-backend ... -model-backend ... -repo ...`. With
host sandboxes it also adds `-sandbox-dir`.

### The service

```ini
[Service]
ExecStart=/home/me/.grain/daemon.sh
Restart=on-failure
RestartSec=10s
KillMode=mixed
TimeoutStopSec=120s
```

- **Restarts**: the unit restarts a daemon that fails, 10 s later. After
  5 failures in 10 minutes (`StartLimitBurst`/`StartLimitIntervalSec`) it
  stops trying rather than spin on a bad flag. Fix the cause, then
  `systemctl --user reset-failed grain && systemctl --user start grain`.
- **Logs**: the user journal, `journalctl --user -u grain -f`. Runs'
  transcripts are in the UI, and their working state is under
  `~/.grain/state` and `~/.grain/sandboxes`.
- **Status**: `systemctl --user status grain`.
- **After logout**: a user unit stops when your last session ends unless
  you enable lingering, once: `loginctl enable-linger "$USER"`.
- **No systemd** (a container, WSL): pass `-install-service=false` and run
  `~/.grain/daemon.sh` yourself, under `tmux` or similar.

Point the CLI at the daemon by adding this to your shell's profile:

```sh
export GRAIN_DATA_DIR=~/.grain
```

The CLI finds the local API token there.

## 4. Add a model connection

Open <http://127.0.0.1:4242> (from another machine: `ssh -L
4242:127.0.0.1:4242 your-workstation`). Under **Settings → Models**
(the gear at the right-hand end of every page's header), add a connection for your model backend with its API key
or sign-in. See [connections-and-models.md](connections-and-models.md).
Until one exists, the daemon logs "the default agent framework is not
ready".

## 5. File a first task

```sh
grain create -title "Add a CONTRIBUTING.md" \
  -body "Write a short CONTRIBUTING.md for this repository." -approve
grain list
```

If the deployment has no git connection yet, the New task dialog and the
Repositories page say so and link to where to add one (step 3): a task on
a repository waits until there is one.

The task targets the first `-repo`, gets a sandbox within one reconcile
tick (10 s), and opens a pull request when it is done. Follow it in the UI
or with `grain get <id>`. For a task that touches no repository, file it
with `-no-repo`.

## Upgrading

The data directory carries over, and the daemon migrates its store on
start.

```sh
# step 1 again with the new VERSION, then:
systemctl --user restart grain
grain version
journalctl --user -u grain -n 50      # confirm it came up
```

`daemon.sh` names the binary by its path, so replacing
`~/.local/bin/grain` in place is enough. If you moved it, re-run `grain
init workstation -non-interactive`. That rewrites `daemon.sh` and the unit,
and keeps your policy. To go back, install the previous version the same
way.

## Uninstalling

```sh
systemctl --user disable --now grain
rm ~/.config/systemd/user/grain.service
systemctl --user daemon-reload
rm ~/.local/bin/grain
rm -rf ~/.grain          # the store, secrets and tokens: back it up first if you may want it again
```
