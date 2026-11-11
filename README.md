# grain

[![Tests](https://github.com/ai-on-gke/grain/actions/workflows/tests.yml/badge.svg)](https://github.com/ai-on-gke/grain/actions/workflows/tests.yml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

Grain is a single-operator agent orchestrator that turns a backlog of software tasks into tested pull requests. For each task, it starts a coding agent in an isolated VM or container sandbox with scoped repository and credential access, watches CI, surfaces questions when blocked, and merges the pull request when checks pass. It is built for engineers and small teams who want to run a queue of tasks unattended rather than supervise one agent session at a time.

<a id="get-started"></a>
## Install

Every install runs the same single-tenant control plane and web UI. You can pass GitHub and model credentials during installation or add them afterwards in **Settings**.

### 1. Linux host (`scripts/setup.sh`)

- **Needs:** Linux with `systemd` and Docker; `/dev/kvm` for [Granule](https://github.com/gke-labs/granule) VM sandboxes (or set `GRAIN_CONTAINER_SANDBOXES=1` for Docker container sandboxes on hosts without KVM).

```bash
git clone https://github.com/ai-on-gke/grain.git
cd grain

sudo \
  GRAIN_GITHUB_TOKEN="<github-token>" \
  GRAIN_OPENAI_API_KEY="<openai-api-key>" \
  GRAIN_TARGET_REPO="owner/repository" \
  ./scripts/setup.sh
```

By default `setup.sh` pulls container images from Docker Hub (`docker.io/grainai/grain`), provisions local PostgreSQL ([docs/database.md](docs/database.md)), and serves the UI on `http://<grain-host>:4242/`. See [docs/host-setup.md](docs/host-setup.md) for release channels, `/var/lib/grain` layout, backups, remote hosts, and uninstalling (`sudo ./scripts/setup.sh --uninstall`), or [docs/grain-init.md](docs/grain-init.md) to build everything from source (`grain init`).

### 2. Docker Hub (`docker.io/grainai/grain`)

- **Needs:** Linux with Docker (plus `systemd` when using `setup.sh`, or user-level `systemd` for a rootless workstation setup). Images are public at `docker.io/grainai/grain` (with sandbox images at `docker.io/grainai/sandbox` and `docker.io/grainai/sandbox-k8s`).

```bash
# Pull from Docker Hub with the host installer:
sudo GRAIN_IMAGE=docker.io/grainai/grain ./scripts/setup.sh

# Or set up a single-user workstation without root for the daemon:
./scripts/install-grain.sh -version v3.2.0 -image docker.io/grainai/grain
~/.local/bin/grain init workstation
```

`grain init workstation` binds the UI on `http://127.0.0.1:4242` and prints or stores the access code under `~/.grain` (`grain access-code -data-dir ~/.grain`). See [docs/quickstart.md](docs/quickstart.md) and [docs/image-signing.md](docs/image-signing.md).

### 3. Kubernetes (`deploy/kubernetes`)

- **Needs:** A Kubernetes cluster with `ReadWriteOnce` storage and `kubectl` (plus a `NetworkPolicy`-capable CNI for `network: none` tasks, or KVM nodes for VM sandboxes).

```bash
# Edit the CHANGEME values in deploy/kubernetes/grain.yaml first
kubectl apply -f deploy/kubernetes/grain.yaml
kubectl -n grain port-forward svc/grain 8080:80
```

Open `http://127.0.0.1:8080` and read the access code with `kubectl -n grain exec deploy/grain -- grain access-code -data-dir /data`, or place `svc/grain` behind an IAP Gateway. Full guide (including `k8s-granule` VM sandboxes): [deploy/kubernetes/README.md](deploy/kubernetes/README.md).

### 4. Google Cloud / Terraform (`deploy/terraform/gcp`)

- **Needs:** A GCP project, `gcloud`, `terraform`, and a GitHub token or GitHub App (Gemini API keys can be minted automatically on GCP or provided directly). Provisions a GCE VM with nested KVM, separate state and sandbox disks, Secret Manager secrets, and an IAP-protected HTTPS load balancer.

```bash
./deploy/terraform/gcp/bootstrap-gcp.sh --project <project-id>
cd deploy/terraform/gcp
cp example.tfvars staging.tfvars && cp backend.hcl.example backend.hcl
# Fill in project_id, deployer_member, and iap_members in staging.tfvars and backend.hcl
terraform init -backend-config=backend.hcl
terraform apply -var-file=staging.tfvars
PROJECT="$(terraform output -raw project_id)" \
  INSTANCE="$(terraform output -raw instance_name)" \
  MINTER_SERVICE_ACCOUNT="$(terraform output -raw minter_service_account)" \
  GRAIN_GITHUB_TOKEN="<github-token>" \
  ./deploy/push-secrets.sh
```

Open `terraform output url` and sign in through Identity-Aware Proxy as a configured principal, or run `terraform output tunnel_command` for local tunnel access with the access code. Full guide: [deploy/terraform/gcp/README.md](deploy/terraform/gcp/README.md).

### First sign-in

Grain has no user accounts or passwords. Whoever holds the deployment's **access code** is the owner ([docs/access-code.md](docs/access-code.md)). Print it on the host with `sudo grain access-code` and paste it once when the browser prompts.

On a `setup.sh` host, reach `http://<grain-host>:4242/` directly, or bind loopback (`GRAIN_UI_ADDR=127.0.0.1:4242`) and tunnel from your workstation:

```bash
./scripts/client.sh <grain-host>
# or manually: ssh -L 4242:localhost:4242 <grain-host>  →  http://localhost:4242
```

To use the `grain` CLI from your workstation against a remote deployment, run `grain login <url>`.

## Use

1. **Connect a GitHub repository.** In **Settings → Git**, add a GitHub token or GitHub App connection if you did not seed `GRAIN_GITHUB_TOKEN` at install time, and add your `owner/repository` under **Repositories** ([docs/git-hosts.md](docs/git-hosts.md)).
2. **Add a model connection.** In **Settings → Models**, add a connection for **Claude Code**, **Codex**, or **Antigravity** (Gemini) and choose your default model ([docs/connections-and-models.md](docs/connections-and-models.md)).
3. **File a first task and watch it run.** Click **New task** in the UI, or queue one from the CLI:

   ```bash
   grain create \
     -title "Add health checks" \
     -body "Add an HTTP health endpoint and cover it with tests." \
     -repo owner/repository \
     -approve

   grain list
   grain logs <id> -follow
   ```

   Grain creates a fresh sandbox, checks out a task branch, and streams live tool calls and logs. If the agent asks a clarifying question or requests a secret, the task parks in **Inbox** (`grain inbox`, `grain reply <id> "<answer>"`).
4. **Review and merge its PR.** The agent pushes commits, opens a pull request, and iterates until CI passes. Review and merge from the task page or GitHub—or pass `-auto-merge` to `grain create` (or enable **Auto-merge** in the UI) so Grain merges automatically once checks are green.
5. **Where to go next:**
   - **Templates and schedules:** define reusable task forms and recurring cron jobs in the UI or in your repo's `.grain/` directory ([docs/declarations.md](docs/declarations.md), [docs/schedules.md](docs/schedules.md)).
   - **Director chat:** open **Director** in the sidebar for a standing interactive agent that can inspect deployment state, debug failures, and propose tasks ([docs/director.md](docs/director.md)).
   - **Settings:** tune sandbox concurrency and caches, pair remote hosts, attach MCP capability servers, or upgrade in place ([docs/package-cache.md](docs/package-cache.md), [docs/host-placement.md](docs/host-placement.md)).

<a id="uninstalling"></a>
<a id="backups"></a>
## More

- **[Architecture](design/design/architecture.design.md)** — control plane, sandboxes, hosts, CI, merge queue, and state layout ([`design/design/`](design/design), [`design/spec/`](design/spec)).
- **[Capabilities](design/design/capabilities.design.md)** — scoped task grants, MCP capability servers ([author's guide](docs/capabilities.md)), and [CI capabilities](docs/ci-capabilities.md).
- **[Agent API](docs/agent-api.md)** — permission scopes (`api:*`), controller MCP routes, and [agent ergonomics](docs/agent-ergonomics.md).
- **[Security](docs/access-code.md)** — access code & IAP, [local policy](docs/local-policy.md), [image signing](docs/image-signing.md), and [security audits](docs/security/audits).
- **[Linux host operations](docs/host-setup.md)** & **[Database](docs/database.md)** — channels, `/var/lib/grain` layout, PostgreSQL/SQLite, backups, remote hosts, and uninstalling.
- **[Releases](docs/releases/README.md)** — release notes, continuous release train, and [standalone CLI archives](docs/release-assets.md).
- **Contributing** — build and test from source with `make build && make test` (Go per [`go.mod`](go.mod) + Node.js/npm), following [Google's Open Source Community Guidelines](https://opensource.google.com/conduct/).
- **License** — [Apache 2.0](LICENSE). *Not an officially supported Google product; not eligible for the Google Open Source Software Vulnerability Rewards Program.*
