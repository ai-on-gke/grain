# Sandbox Image Contract & Minimal Base Images

Grain publishes two tiers of sandbox images for both Granule microVMs and container/Kubernetes pod sandboxes:

1. **Minimal base images** (`ghcr.io/bwsalmon/grain/guest-base:<tag>` and `ghcr.io/bwsalmon/grain/k8s-sandbox-base:<tag>`) carrying **only** what stock Granule and Grain's own runtime boundary need to boot and drive a sandbox.
2. **Full default images** (`ghcr.io/bwsalmon/grain/guest:<tag>` and `ghcr.io/bwsalmon/grain/k8s-sandbox:<tag>`) layered `FROM` those minimal bases with language toolchains, Docker, OCR/attachment packages, and agent CLIs.

Every base and full sandbox image carries an explicit integer contract version (`dev.grain.sandbox.contract` OCI label and `/etc/grain/sandbox-contract` file, modeled directly on Granule's `dev.granule.guest.contract` / `/etc/granule/guest-contract` in `third_party/granule/pkg/guestimage/contract.go`).

If you maintain a custom sandbox image `FROM ghcr.io/bwsalmon/grain/guest-base:<tag>` or `FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:<tag>`, **you do not need to rebuild your image on every Grain commit** — you only need to update your base image when `dev.grain.sandbox.contract` (or, for Granule VMs, `dev.granule.guest.contract`) bumps.

---

## 1. Minimal Base Images vs Full Default Images

> **What you inherit:** `ghcr.io/bwsalmon/grain/guest-base:<tag>` and
> `ghcr.io/bwsalmon/grain/k8s-sandbox-base:<tag>` **already satisfy every
> requirement of the sandbox contract** — the `debian` user (`uid 1000`),
> the `/home/agent` symlink, `git`, `curl`, `ca-certificates`,
> `python3-minimal` for port forwarding, the contract version stamps and,
> on Granule microVMs, passwordless `sudo`, `systemd` and `grain-connect`.
> None of that is yours to set up.
>
> **What you do:** start `FROM` the base image for your backend and add
> your own `RUN apt-get install ...` steps for the packages and toolchains
> your tasks need. [The reference list](#4-reference-boundary-properties-provided-by-the-base-images)
> below says what the base images provide, so you know what not to remove.

### Granule microVM (`guest-base` vs `guest`)

| Layer | Image / Stage | Contents |
| :--- | :--- | :--- |
| **Minimal Base** | `ghcr.io/bwsalmon/grain/guest-base:<tag>` (`Granulefile.guest-base`) | Stock Granule Debian Bookworm VM base (`systemd-sysv`, `udev`, `kmod`, `iproute2`, `acpid`, `e2fsprogs`, `linux-image-$(dpkg --print-architecture)` with `RESUME=none`, `klibc-utils`, `libnss-myhostname`, `00-eth0.link`, `/tmp` tmpfs, `grain-growfs`, `VM PACK --variant=debian` with `granule-agent`, `granule-mem-agent`, `granule-growfs`, and `/etc/granule/guest-contract`) **plus** Grain's minimal boundary: user `debian` (`uid 1000`, `/home/debian`, `/bin/bash`), `/home/agent -> /home/debian`, passwordless `sudo`, `kvm` group, `git`, `curl`, `ca-certificates`, `python3-minimal` (`os, socket, sys, threading` for `GuestForwarder`), `/usr/local/bin/grain-connect` (`/run/grain` tmpfiles.d), core POSIX utilities (`procps`, `findutils`, `grep`, `sed`, `gawk`, `tar`, `coreutils`), and `/etc/grain/sandbox-contract`. |
| **Default Full** | `ghcr.io/bwsalmon/grain/guest:<tag>` (`Granulefile`) | `FROM guest_base_rootfs` + developer convenience packages (`build-essential`, `python3`, `python3-venv`, `pipx`, `jq`, `ripgrep`, `fd-find`, `shellcheck`, `file`, `openssh-client`, `tmux`, `unzip`, `xz-utils`, `bubblewrap`, `gnupg`), OCR & attachment tools (`tesseract-ocr`, `tesseract-ocr-eng`, `imagemagick`, `poppler-utils`, `fonts-urw-base35`, `fonts-dejavu-core`), Docker CE & containerd, `kind`, `kubectl`, `google-cloud-cli`, `terraform`, Go, Node.js, Playwright browser cache, `act`, and `actionlint`. |

### Container & Kubernetes Pod (`k8s-sandbox-base` vs `k8s-sandbox`)

| Layer | Image / Stage | Contents |
| :--- | :--- | :--- |
| **Minimal Base** | `ghcr.io/bwsalmon/grain/k8s-sandbox-base:<tag>` (`Dockerfile.k8s-sandbox` `--target k8s-sandbox-base`) | `debian:bookworm-slim` + `bash`, `ca-certificates`, `curl`, `findutils`, `git`, `tar`, `python3-minimal` (`os, socket, sys, threading` for `GuestForwarder`), user `debian` (`uid 1000`, `/home/debian`, `/bin/bash`), symlink `/home/agent -> /home/debian`, and `/etc/grain/sandbox-contract`. |
| **Default Full** | `ghcr.io/bwsalmon/grain/k8s-sandbox:<tag>` (`Dockerfile.k8s-sandbox`) | `FROM k8s-sandbox-base` + OCR & attachment tools (`tesseract-ocr`, `tesseract-ocr-eng`, `imagemagick`, `poppler-utils`, `fonts-urw-base35`, `fonts-dejavu-core`) + the three pinned agent CLIs (`claude`, `agy`, `codex`). |

---

## 2. Building a Custom Sandbox Image

Starting `FROM ghcr.io/bwsalmon/grain/guest-base:<tag>` or
`FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:<tag>` **already satisfies
every requirement of the contract**. The only steps you add are your own
`RUN apt-get install ...` lines (or other toolchain installs). Do not
remove or replace anything the base image carries — see
[§4](#4-reference-boundary-properties-provided-by-the-base-images) for the
list — and run `grain sandbox-image check <image>` before you use it.

### Custom Granule microVM Image (`FROM guest-base`)

Because `guest-base` is a packed Granule VM image that already includes the kernel, `systemd`, Granule guest agents, `debian` user (`uid 1000`), `python3-minimal`, and `/usr/local/bin/grain-connect`, a custom `Granulefile` unpacks `guest-base`, installs only the packages your repository needs, and re-packs:

```dockerfile
FROM ghcr.io/bwsalmon/grain/guest-base:latest AS rootfs
VM UNPACK

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential \
        jq \
        ripgrep \
    && rm -rf /var/lib/apt/lists/*

VM PACK --variant=debian AS custom_guest
LABEL dev.grain.sandbox.contract="2"
```

Build and check it with:

```bash
granule granulefile build -f Granulefile -t my-registry.example.com/team/sandbox-guest:v1 .
grain sandbox-image check --backend granule my-registry.example.com/team/sandbox-guest:v1
```

### Custom Container / Kubernetes Sandbox Image (`FROM k8s-sandbox-base`)

Start `FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:<tag>`, switch to `USER root` to install packages or agent CLIs, and switch back to `USER debian` (`WORKDIR /home/debian`):

```dockerfile
FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:latest
USER root

RUN apt-get update && apt-get install -y --no-install-recommends \
        jq \
        ripgrep \
    && rm -rf /var/lib/apt/lists/*

USER debian
WORKDIR /home/debian
```

Build and verify both the OCI labels and the in-container runtime boundary with:

```bash
docker build -t my-registry.example.com/team/k8s-sandbox:v1 .
grain sandbox-image check --backend k8s --probe my-registry.example.com/team/k8s-sandbox:v1
```

---

## 3. When the Base Image Has to Change

### Two Integer Contracts

1. **`dev.grain.sandbox.contract`** (`/etc/grain/sandbox-contract`, defined in `pkg/sandboximage/contract.go`):
   - `Contract` (current: `2`): the version of the Grain-to-sandbox boundary this build of Grain stamps and expects.
   - `MinimumContract` (current: `2`): the oldest sandbox contract version this build of Grain can still drive.
2. **`dev.granule.guest.contract`** (`/etc/granule/guest-contract`, defined in `third_party/granule/pkg/guestimage/contract.go`, Granule microVM backends only):
   - `Contract` (current: `1`): the version of the Granule VMM-to-guest boundary (`granule-agent`, vsock port 1024, kernel command line, `granule-configure-*` units).
   - `MinimumContract` (current: `1`): the oldest Granule guest contract version the bundled Granule runtime can boot.

### Versioning Rules

- **Ordinary Grain commits do NOT bump `Contract`**: changes to the daemon, UI, task store, or optional tools in the full `guest` / `k8s-sandbox` images leave `Contract` unchanged. Custom images built `FROM guest-base` or `FROM k8s-sandbox-base` continue to satisfy the contract without rebuilding.
- **Additive boundary changes bump `Contract` while keeping `MinimumContract`**: when Grain adds a new optional in-sandbox helper that the daemon can fall back without, `Contract` increments and `MinimumContract` stays at the previous version. Existing custom images remain supported (`ContractSupported`), while `grain sandbox-image check` notes that a newer contract version is available.
- **Breaking boundary changes bump both `Contract` and `MinimumContract`**: when the daemon can no longer drive an image built against an older contract (for example, an incompatible change to `grain-connect`, `GuestForwarder`, or `dev.granule.guest.contract`), `MinimumContract` is raised. `grain sandbox-image check` reports `too-old` (`NeedsBaseRebuild: true`) and instructs the operator to rebuild `FROM` a current `guest-base` or `k8s-sandbox-base`.

### Using a Custom Image (Settings → Sandboxes → Custom sandbox image)

Push the image somewhere every sandbox host (or the cluster) can pull it
from, then name it in **Settings → Sandboxes → Custom sandbox image**
(`grain_config.sandbox_image` in the state repository, or
`grain settings -sandbox-image <ref>`). It applies to whichever backend is
active: a packed Granule guest on `granule` and `kubernetes-granule`, a
container image on `kubernetes` and `container`. Leaving it empty uses
the image this build of grain is stamped for.

- **Live.** The next sandbox created uses the new image; sandboxes already
  running keep theirs. Nothing is built on the daemon host, because the
  backend pulls the image the same way it pulls the stock one. Changing
  the image also starts a new CI cache epoch, so job results cached under
  the old image are not replayed.
- **Explicit flags still win.** `-sandbox-image` or a `-granule-create-arg
  -kontur-image` pin on `granule`, or a non-default
  `-container-sandbox-image` on `container`, is used instead of the
  setting.
- **Checked against the contract, not the commit.** The stock image is tied
  to the daemon's commit, and pinning another tag of it still produces the
  drift warning. A custom image is expected to come from another commit. It
  gets a warning, in the Settings pane and in the daemon log, only when
  its contract labels are missing or unreadable, older than this grain's
  current contract, or newer than this grain knows. The warning also
  appears when grain cannot read the labels at all, for example a private
  registry the daemon has no credentials for; check those images with
  `grain sandbox-image check`.
- **A copy of the stock image is not a custom image.** When
  `sandbox_image`, `k8s_sandbox_image` or `k8s_granule_image` names grain's
  own stock image (the same `<owner>/grain/guest` or
  `<owner>/grain/k8s-sandbox` repository, in any registry) at a
  `sha-<short>` or `vX.Y.Z` tag other than the one this daemon is stamped
  for, the pin holds every sandbox at that build while upgrades move the
  daemon on. On a moving tag, the same applies when its
  `org.opencontainers.image.revision` label names a different commit.
  Settings shows a warning beside the field, and **System → Sandboxes**
  shows one for each pinned setting. Clear the setting to follow the
  stamp, or move the pin to the stamped tag.

This setting replaces the old **Sandbox setup script**
(`grain_config.sandbox_setup_script` / `scripts/guest-customization.sh`).
That was a shell script the daemon ran inside the stock guest at dispatch
time. Schema 40 drops it, and a state repository that still carries it
loads without it. To keep what such a script installed, put the same
commands in a `RUN` step of a Dockerfile `FROM` the base image above.

---

## 4. Reference: Boundary Properties Provided by the Base Images

Everything below is **already baked into `guest-base` and
`k8s-sandbox-base`**: an image built `FROM` either of them carries it
without any work on your part. It is listed here so you know what not to
remove from a custom image, and what `grain sandbox-image check --probe`
(`VerifyScript` in `pkg/sandboximage/contract.go`) verifies if you build
an image from scratch instead. `grain sandbox-image contract` prints the
same list for the running build of grain.

### Every backend (`guest-base` and `k8s-sandbox-base`)

| Property | Why grain relies on it |
| :--- | :--- |
| User `debian` (`uid 1000`, home `/home/debian`, shell `/bin/bash`) | Tasks run as this user; the checkout lives at `/home/debian/work`. |
| Symlink `/home/agent -> /home/debian` | Agent frameworks and older paths name the home directory `/home/agent`. |
| `git` | Clones the task's repository and pushes its branch through the git proxy. |
| `curl` | Reaches the in-sandbox namespace and git proxy at `127.0.0.1:9728`. |
| `ca-certificates` (`/etc/ssl/certs/ca-certificates.crt`) | TLS to registries, package mirrors and model APIs. |
| `python3-minimal` (`os`, `socket`, `sys`, `threading`) | `GuestForwarder`, the capability port forwarder. |
| `/usr/local/bin/grain` (`cmd/grain-sandbox`) offering `grain ls`, `read`, `grep`, `ci`, `pr`, `task`, `api` and `host-script` | The sandbox CLI agents reach their deployment, CI and pull request through (`docs/design/sandbox-first-agent-surface.md`); the orchestrator no longer offers those as MCP tools. `grain sandbox-image check -probe` runs `GRAIN_SANDBOX_CLI=0 /usr/local/bin/grain help` and fails an image whose `grain` does not list them. |
| `dev.grain.sandbox.contract` label and `/etc/grain/sandbox-contract` | The contract version grain checks the image against. |

### Granule microVM backends only (`guest-base`)

| Property | Why grain relies on it |
| :--- | :--- |
| `sudo` (passwordless for user `debian`) | Agents install missing packages with `sudo apt-get install`. |
| `systemd` | Boots the guest and runs Granule's guest units. |
| `/usr/local/bin/grain-connect` (`/run/grain` tmpfiles.d) | Connects the guest to the daemon. |
| `dev.granule.guest.contract` label and `/etc/granule/guest-contract` | Granule's own guest contract (`granule-agent`, vsock port 1024). |

---

## 5. CLI Reference

```bash
# Print the default full sandbox image stamped into this grain build:
grain sandbox-image
grain k8s-sandbox-image

# Print the corresponding minimal base image (guest-base / k8s-sandbox-base):
grain sandbox-image -base
grain k8s-sandbox-image -base

# Print the current sandbox contract versions, base image references, and required boundary items:
grain sandbox-image contract
grain sandbox-image contract -json

# Check an image's contract labels (and optionally run VerifyScript inside it with -probe):
grain sandbox-image check <image>
grain sandbox-image check -backend granule <image>
grain sandbox-image check -backend k8s -probe <image>
grain sandbox-image check -json <image>
```

---

## 6. Contract History

| Version | `MinimumContract` | `dev.granule.guest.contract` | Summary & Required Action for Custom Images |
| :--- | :--- | :--- | :--- |
| **1** | **1** | **1** | Initial versioned Grain sandbox contract. Base images `ghcr.io/bwsalmon/grain/guest-base` and `ghcr.io/bwsalmon/grain/k8s-sandbox-base` carry `dev.grain.sandbox.contract="1"` and `/etc/grain/sandbox-contract` (`"1\n"`), user `debian` (`uid 1000`, `/home/debian`), `/home/agent -> /home/debian`, `git`, `curl`, `ca-certificates`, `python3-minimal` (`os, socket, sys, threading` for `GuestForwarder`), and on Granule microVMs `systemd`, passwordless `sudo`, and `/usr/local/bin/grain-connect`. |
| **2** | **2** | **1** | In-sandbox grain CLI. Every base image carries `/usr/local/bin/grain` (`cmd/grain-sandbox`) with the sandbox CLI (`grain ls`, `read`, `grep`, `ci`, `pr`, `task`, `api`) beside `grain host-script`, because the orchestrator now strips those tools from MCP and has agents run them in the sandbox. `MinimumContract` rises to 2: a v1 image may carry a `host-script`-only `grain`, which leaves an agent unable to read its own CI or pull request. **Rebuild custom images `FROM` a current `guest-base` or `k8s-sandbox-base`.** |
