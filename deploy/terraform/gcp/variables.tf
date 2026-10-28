# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This module deploys a *staging* environment of v2 (bwsalmon/agents#394):
# one small VM running `grain daemon` (README.md, "Deploying it"), its
# SQLite store and secrets on a separate persistent disk so the VM itself
# is disposable, exposed at a fixed DNS name through an IAP-protected
# HTTPS load balancer, with the service accounts v2's gcp-key/gemini-key
# capabilities need (plus, for staging, room to create VMs and GKE
# clusters too).
#
# Unlike v1's module (a controller running nested libvirt guests, meant
# to be forked as a generic, any-org deployment from a template)
# this module is still just the one VM scripts/setup.sh already knows
# how to install and update -- no separate controller, no fleet of
# sandbox VMs Terraform itself creates or tears down. But it is no longer
# narrower in the way it once was: v2's daemon no longer only dispatches
# into plain host directories on that one VM. enable_granule_sandboxes (on
# by default, bwsalmon/agents#504) opts into orchestrator.GranuleSandboxes
# instead -- one real bwsalmon/granule-managed VM per slot, nested inside
# this same host via /dev/kvm (enable_nested_virtualization, also on by
# default) -- so README.md's own "no host adapter yet" is about there
# being no separate fleet-management layer, not about there being no VM
# isolation at all any more. See variables.tf's own "granule" section and
# this module's README, "Granule sandboxing", for what a first apply with
# that on needs beyond `terraform apply` itself.
#
# Nothing secret lives here, the same split v1's variables.tf
# documents: the GitHub token, the GCP key-minter's own key (if minted),
# and the granule SSH private key (if enable_granule_sandboxes) are never
# Terraform inputs -- see secrets.tf and push-secrets.sh for how they
# reach the VM instead.

# ---------------------------------------------------------------- project --

variable "project_id" {
  type        = string
  description = "GCP project that holds the host VM, its disks, the load balancer, and its service accounts."
}

variable "region" {
  type        = string
  description = "Region for the subnet, the router, and the managed instance's zone."
  default     = "us-central1"
}

variable "zone" {
  type        = string
  description = "Zone for the host VM and its data disk."
  default     = "us-central1-a"
}

variable "name_prefix" {
  type        = string
  description = "Prefix for every resource name, so more than one deployment can share a project."
  default     = "grain"

  validation {
    condition     = can(regex("^[a-z]([-a-z0-9]{0,20}[a-z0-9])?$", var.name_prefix))
    error_message = "name_prefix must be a lowercase RFC1035 label, 22 characters or fewer."
  }
}

variable "labels" {
  type        = map(string)
  description = "Labels applied to the instance, disks, and service accounts."
  # No `environment` key by default: this module backs both a main and a
  # staging deployment, so a default naming either one would be wrong for
  # the other. Each config repo sets it.
  default = {
    managed-by = "terraform"
    system     = "grain"
  }
}

# ------------------------------------------------------------------- host --

variable "machine_type" {
  type        = string
  description = <<-EOT
    Host machine type. With enable_nested_virtualization on (the
    default) this must be a family that supports it -- N1, N2, N2D, C2,
    C3, C4 or M-series, never E2, the same constraint terraform/gcp's v1
    host carries. Turn that off, for a deployment dispatching only into
    host directories, and any family works including E2.

    n2-standard-2 (2 vCPU, 8 GB) is comfortably enough to build grain
    (`make container-build`: a Go compile plus a Vite frontend build,
    both inside Docker) and run one daemon against a handful of test
    repos.

    Size up for real agent work, though. Because dispatch lands in host
    directories rather than in a sandbox guest of its own, whatever an
    agent does -- a build, a docker daemon, a kind cluster -- runs on
    *this* machine, alongside the daemon. The default covers grain's own
    build and not much more; a deployment whose tasks compile or test
    anything substantial wants several times it.
  EOT
  default     = "n2-standard-64"
}

variable "enable_nested_virtualization" {
  type        = bool
  description = <<-EOT
    Give the host /dev/kvm, for a deployment whose sandboxes are VMs
    rather than plain host directories -- a granule-managed guest
    (pkg/granule) needs it; a dispatch into a host directory does not.

    Two things follow from it, both handled in instance.tf rather than
    left to you. The machine family must support nested virtualization
    -- N1, N2, N2D, C2, C3 or M-series, never E2 -- which a precondition
    checks, because GCP's own failure for an E2 names the maintenance
    policy instead and reads as an unrelated problem. And
    on_host_maintenance becomes TERMINATE, matching terraform/gcp's v1
    host for the reason its own comment gives: nested virtualization and
    live migration have a history. Turning this off gets MIGRATE back,
    and the daemon then rides out host maintenance rather than being
    terminated and reconverging.
  EOT
  default     = true
}

variable "boot_image" {
  type        = string
  description = "Boot image for the host. v2's own scripts/setup.sh assumes Debian 12."
  default     = "debian-cloud/debian-12"
}

variable "boot_disk_gb" {
  type        = number
  description = <<-EOT
    Host boot disk: the OS, the grain checkout, the journal, and nothing
    that must survive a redeploy -- the SQLite store and secrets live on
    the separate data disk instead (see data_disk_gb), which is the whole
    point of splitting them: this disk can be recreated from scratch (a
    new boot_image, a bigger machine_type) with no state lost.

    Deliberately modest, and it stays that way, because nothing that
    grows with the work done on this host is here any more. Sandboxes --
    the images, the per-VM disk overlays and the per-run checkouts --
    have a volume of their own (sandbox_disk_gb), so this one holds a
    Debian install and a few hundred megabytes of grain. What it buys is
    the thing a shared disk cannot: a task that fills its disk stops
    tasks, not the OS, the journal, config-sync or the daemon.

    Set sandbox_disk_gb = 0 and all of that comes back here, at which
    point this needs to be far larger -- a single granule sandbox image is
    several gigabytes before any VM has written a byte.
  EOT
  default     = 100
}

variable "data_disk_gb" {
  type        = number
  description = <<-EOT
    Persistent disk mounted at /var/lib/grain/data -- scripts/setup.sh's
    own $GRAIN_DATA_DIR default, so no override is needed for the daemon
    to find it there, and the one disk to back up. Holds the embedded SQLite store
    (pkg/model/sqlite) and the secrets database and credential files
    (pkg/secrets) -- state a redeploy must not lose. 20 GB is generous
    for a staging deployment working against a handful of test repos;
    grow it if the store becomes the bottleneck. Nothing a sandbox writes
    lives here -- that is sandbox_disk_gb's disk, and keeping the two
    apart is what stops a task's runaway checkout from wedging the store
    the whole deployment reads and writes.
  EOT
  default     = 100
}

variable "data_disk_type" {
  type        = string
  description = "pd-balanced is the sane default for a staging workload; pd-ssd if I/O becomes the bottleneck."
  default     = "pd-balanced"
}



variable "sandbox_disk_gb" {
  type        = number
  description = <<-EOT
    Persistent disk for everything a sandbox writes, mounted by
    files/startup.sh at /mnt/grain-sandbox and holding two things:

      * docker's data root (/mnt/grain-sandbox/docker), which is where
        the sandbox image and every granule VM's writable root actually
        live -- granule gives a VM its own disk as a qcow2 overlay
        created *inside* that VM's container (bwsalmon/granule#37), so
        everything a task's guest writes lands in docker's storage;
      * /mnt/grain-sandbox/sandboxes, bind-mounted onto scripts/setup.sh's
        own $GRAIN_SANDBOX_DIR (/var/lib/grain/scratch): the per-run
        checkouts orchestrator.HostSandboxes makes when
        enable_granule_sandboxes is off, and the package and Actions
        caches.

    This is where the bulk of the host's storage belongs, and the reason
    it is a volume of its own is isolation rather than size: a task that
    fills it costs the runs in flight, while the OS, the journal,
    config-sync, the daemon and the store on their own disks carry on --
    including the UI that has to be reachable to say what went wrong.
    Nothing here is state: a wipe costs a re-pulled image and some
    already-dead runs' leftovers, so unlike the data disk it carries no
    prevent_destroy.

    0 means no separate disk: all of the above goes back on the boot
    disk, which then has to be sized for it (see boot_disk_gb). Attaching
    or detaching one takes effect at the host's next boot, since
    files/startup.sh is what mounts it -- or immediately, over
    `sudo google_metadata_script_runner startup`.
  EOT
  default     = 1024
}

variable "sandbox_disk_type" {
  type        = string
  description = <<-EOT
    pd-balanced, matching data_disk_type: a sandbox's I/O is a git clone,
    a package install and a build, and pd-balanced's throughput scales
    with a disk this size. pd-ssd if a task's build turns out to be
    seek-bound; pd-standard is a false economy here, since every VM boot
    reads its image back off this disk.
  EOT
  default     = "pd-balanced"
}

variable "enable_shielded_vm" {
  type        = bool
  description = <<-EOT
    Shielded VM (secure boot, vTPM, integrity monitoring). Unlike
    terraform/gcp's v1 host, this VM has no /dev/kvm dependency to
    conflict with it (see machine_type), so it defaults on here.
  EOT
  default     = true
}

variable "deploy_generation" {
  type        = string
  description = <<-EOT
    Opaque token folded into instance metadata's grain-deploy-generation
    alongside a hash of grain_config itself (instance.tf); files/config-
    sync.sh on the host watches that combined value and redeploys
    whenever it changes. CI (or a human applying this by hand) sets this
    variable to the commit SHA of whatever grain_ref names, which is what
    makes a push roll out; the grain_config hash is what makes a change
    to a value inside grain_config (slots, target_repos, ...) roll out
    too, even on a manual `terraform apply` that never touches
    deploy_generation at all (bwsalmon/agents#592).
  EOT
  default     = "manual"
}

# ---------------------------------------------------------------- network --

variable "create_network" {
  type        = bool
  description = "Create a VPC and subnet. Set false to attach to an existing one."
  default     = true
}

variable "network_name" {
  type        = string
  description = "Existing network to attach to when create_network is false."
  default     = ""
}

variable "subnetwork_name" {
  type        = string
  description = "Existing subnet to attach to when create_network is false."
  default     = ""
}

variable "subnet_cidr" {
  type        = string
  description = "CIDR for the created subnet."
  default     = "10.30.0.0/24"
}

variable "assign_external_ip" {
  type        = bool
  description = <<-EOT
    Give the host its own public address. Off by default: the host is
    reached two ways either way -- the IAP-protected HTTPS load balancer
    (dns.tf, iap.tf) for the UI, and an IAP TCP tunnel for SSH
    (ssh_source_ranges) -- neither needs the instance to have an
    external IP of its own. Leave enable_cloud_nat on for its own
    outbound reach (GitHub, the Debian mirror, Docker Hub, the Gemini
    API).
  EOT
  default     = false
}

variable "enable_cloud_nat" {
  type        = bool
  description = "Create a Cloud Router and NAT so the host has egress despite assign_external_ip = false."
  default     = true
}

variable "ssh_source_ranges" {
  type        = list(string)
  description = <<-EOT
    Who may reach port 22 on the host. The default is Google's IAP
    range, so SSH goes through `gcloud compute ssh --tunnel-through-iap`
    and port 22 is never open to the internet.
  EOT
  default     = ["35.235.240.0/20"]
}

variable "enable_os_login" {
  type        = bool
  description = "IAM-driven SSH auth (roles/compute.osLogin) instead of manually managed keys."
  default     = true
}

variable "ui_port" {
  type        = number
  description = <<-EOT
    Port `grain daemon` serves the UI/API on, and the load balancer's
    backend port. Like scripts/setup.sh's own GRAIN_UI_ADDR default
    (0.0.0.0:4242) it binds every interface, but on ui_port, the port
    the load balancer and firewall below are built around: an external HTTPS load balancer
    reaches a backend over its *internal* IP, which loopback cannot
    answer on. That is safe here specifically because network.tf's
    firewall rule for this port only admits Google's own health-check
    and load-balancing source ranges (130.211.0.0/22, 35.191.0.0/16),
    not the internet at large -- the actual access control is IAP, on
    the load balancer in front of it (iap.tf), same as it would be for a
    loopback-bound service reached over a tunnel.
  EOT
  default     = 8080
}

variable "expose_ui_publicly" {
  type        = bool
  description = <<-EOT
    Put the UI behind a public HTTPS load balancer, protected by IAP
    (iap.tf), reachable at a fixed DNS name from any browser.

    Turn it off for a tunnel-only deployment. Nothing public is created
    then -- no load balancer, no reserved address, no managed
    certificate, no DNS name -- and the UI is reached by forwarding
    ui_port over IAP's TCP tunnel instead:

        gcloud compute start-iap-tunnel <instance> <ui_port> \
          --local-host-port=localhost:8080 --zone <zone>

    then http://localhost:8080. See the tunnel_command output, which
    fills that in for you.

    The access control is equivalent, not weaker: both paths are IAP,
    differing in which grant they check --
    roles/iap.httpsResourceAccessor on the backend service for the load
    balancer, roles/iap.tunnelResourceAccessor for the tunnel. What
    changes is what exists: tunnel-only has no public entry point at
    all, no third-party DNS dependency (variables.tf's own
    dns_managed_zone explains the sslip.io default), and none of the
    certificate provisioning wait a first apply otherwise spends.

    It is also most of this module's running cost -- the load balancer
    and its managed certificate cost more than the VM. On for
    compatibility; a deployment serving two named people probably wants
    it off.
  EOT
  default     = true
}

# ------------------------------------------------------------------- IAM ---

variable "agent_account_id" {
  type        = string
  description = <<-EOT
    account_id (not email) of the narrow service account the gcp-key and
    gemini-key capabilities (pkg/capability/gcpkey,
    pkg/capability/geminikey) mint task credentials for. Defaults to
    pkg/gcpsetup.DefaultAgentAccountID -- the same name `grain setup gcp`
    would create on a fresh install -- purely so a deployment that only
    ever runs one of these per project needs no extra daemon flag beyond
    what deploy.sh already sets from this module's own output. Override
    it to run more than one v2 deployment (staging and something else)
    in the same GCP project.
  EOT
  default     = "grain-agent"
}

variable "minter_account_id" {
  type        = string
  description = <<-EOT
    account_id of the standing service account that mints and revokes
    the agent account's per-task keys, and (with enable_gemini_key)
    administers API keys project-wide. Defaults to
    pkg/gcpsetup.DefaultMinterAccountID for the same reason
    agent_account_id does.
  EOT
  default     = "grain-gcp-key-minter"
}

variable "enable_gemini_key" {
  type        = bool
  description = <<-EOT
    Grants the minter account roles/serviceusage.apiKeysAdmin on the
    project and enables generativelanguage.googleapis.com and
    apikeys.googleapis.com -- what pkg/capability/geminikey needs to
    mint a short-lived Gemini API key for a task that asks for one. On
    by default: bwsalmon/agents#394 asks this staging deployment to be
    able to hand out Gemini keys.
  EOT
  default     = true
}

variable "agent_can_manage_compute_instances" {
  type        = bool
  description = <<-EOT
    Grants the agent account create/delete/start/stop and SSH access to
    Compute Engine instances -- roles/compute.instanceAdmin.v1,
    roles/compute.osLogin, and (project-wide, see iam.tf's own comment
    on why it cannot be conditioned the same way) roles/iap.tunnelResourceAccessor
    -- everywhere in the project except this deployment's own host VM.
    Also enables compute.googleapis.com. On by default: bwsalmon/agents#394
    asks this staging deployment to be able to create VMs.
  EOT
  default     = true
}

variable "agent_can_manage_gke" {
  type        = bool
  description = <<-EOT
    Grants the agent account roles/container.admin and
    roles/artifactregistry.admin, project-wide, plus
    roles/iam.serviceAccountUser on itself (needed to attach its own
    identity to a GKE node pool -- see iam.tf's own comment,
    bwsalmon/agents#146 found this live for v1).
    Also enables container.googleapis.com and
    artifactregistry.googleapis.com. On by default: bwsalmon/agents#394
    asks this staging deployment to be able to create GKE clusters.
  EOT
  default     = true
}

variable "agent_can_manage_cloud_sql" {
  type        = bool
  description = <<-EOT
    Grants the agent account roles/cloudsql.admin project-wide to create
    and manage Cloud SQL database instances, databases, and users.
    Also enables sqladmin.googleapis.com. On by default: asks this staging
    deployment to be able to manage Cloud SQL databases.
  EOT
  default     = true
}

variable "enforce_private_infrastructure_policies" {
  type        = bool
  description = <<-EOT
    Enforce GCP organization policy constraints on the project to restrict
    public endpoints on GKE clusters (constraints/gke.restrictPublicEndpoints),
    deny external IP allocation for VM instances and GKE node pools
    (constraints/compute.vmExternalIpAccess), and restrict public IP
    on Cloud SQL instances (constraints/sql.restrictPublicIp). On by default
    to guarantee agent tasks provision private infrastructure only.
  EOT
  default     = true
}

variable "deployer_member" {
  type        = string
  description = <<-EOT
    The identity applying this Terraform (e.g. "user:you@example.com" or
    "serviceAccount:ci@project.iam.gserviceaccount.com"), granted
    roles/iam.serviceAccountKeyAdmin on the minter account so that
    push-secrets.sh -- run separately, after `terraform apply`, by this
    same identity -- can mint the minter's own key without that key ever
    passing through Terraform state, and
    roles/secretmanager.secretVersionManager on each secret in secrets.tf
    so it can write them (the same reasoning
    v1's iam.tf's deployer_manages_agent_keys already applied to
    v1). Required whenever a key needs minting at all: enable_gemini_key
    or agent_can_manage_compute_instances or agent_can_manage_gke or
    agent_can_manage_cloud_sql, since each needs the agent account to work
    with a real key in place.
  EOT
  default     = ""
}

# ----------------------------------------------------------------- grain ---

variable "grain_repo_url" {
  type        = string
  description = "Clone URL for grain itself."
  default     = "https://github.com/ai-on-gke/grain"
}

variable "grain_ref" {
  type        = string
  description = <<-EOT
    Branch of grain to deploy. Names two things that move together: the
    checkout files/deploy.sh keeps on the host (which is where
    scripts/setup.sh itself, and scripts/granule's own image builds,
    come from), and -- unless grain_image_tag overrides it -- which
    published image the daemon runs, since CI tags one per branch.

    A tag or commit SHA still works for the checkout, but has no image
    published under that name: set grain_image_tag to sha-<short sha>
    alongside it to pin the running build (see that variable).
  EOT
  default     = "main"
}

variable "grain_image" {
  type        = string
  description = <<-EOT
    Image repository, with no tag, that grain-daemon.service runs --
    grain plus every binary it shells out to, built and pushed on every
    commit by .github/workflows/build-artifacts.yml (Dockerfile).

    Override it to deploy from a mirror or a private copy; a registry
    that needs credentials also needs grain_image_pull_user and a token
    pushed by push-secrets.sh.
  EOT
  default     = "ghcr.io/bwsalmon/grain/grain"
}

variable "grain_image_tag" {
  type        = string
  description = <<-EOT
    Which tag of grain_image to run. Empty (the default) follows
    grain_ref: the branch tag CI publishes for it, which is that branch
    name with "/" replaced by "-".

    Set it to sha-<short sha> to pin this deployment to one immutable
    build, and to roll one back: every commit publishes such a tag, so a
    rollback is this variable plus a `terraform apply`, with no rebuild
    anywhere.
  EOT
  default     = ""
}

variable "grain_image_pull_user" {
  type        = string
  description = <<-EOT
    Username for `docker login` against grain_image's registry, for a
    private package. The token half is a secret and never a Terraform
    input -- push-secrets.sh puts it in Secret Manager, exactly as it
    does for the GitHub PAT.

    Empty (the default) pulls anonymously, which is what
    ghcr.io/bwsalmon/grain's own public package needs.
  EOT
  default     = ""
}

variable "test_repos" {
  type        = list(string)
  description = <<-EOT
    owner/name of the handful of test repos this staging deployment
    works against -- what "assume a scoped PAT to a few test repos"
    (bwsalmon/agents#394) means in practice: the PAT itself (pushed by
    push-secrets.sh, never a Terraform input) is a GitHub fine-grained
    token scoped, on GitHub's side, to exactly these repositories, which
    is one enforcement boundary. This is now also the source of the
    daemon's own -target-repos allow-list (pkg/model.Config.TargetRepos,
    bwsalmon/agents#399), the same "allowlist a task naming anything else
    is parked with a comment rather than dispatched against" v1's
    own target_repos documented -- so a task
    naming a repo outside this list is refused by grain itself, not
    just by GitHub declining the PAT's own reach. Empty leaves the
    daemon's own allow-list empty too, i.e. unrestricted, the same
    default_target_repo's own validation below already treats it.
  EOT
  default     = []
}

variable "default_target_repo" {
  type        = string
  description = "owner/name a task with no /repo directive targets. Empty leaves the daemon with no default."
  default     = ""
}

variable "credential_name" {
  type        = string
  description = "secrets/github credential name the scoped PAT is stored under -- scripts/setup.sh's own GRAIN_GITHUB_CREDENTIAL_NAME."
  default     = "bot"
}

variable "github_host" {
  type        = string
  description = "GitHub API host. Override only to point staging at a mock server."
  default     = "github.com"
}

variable "agy_path" {
  type        = string
  description = <<-EOT
    Path to the Antigravity CLI (agy) the default agent framework runs as
    a subprocess. Empty resolves "agy" on $PATH, which is what a host
    with a normal install wants; set it when agy lives somewhere off
    $PATH (its own installer targets ~/.gemini/bin/agy). A deployment
    running agent_framework "claude" instead does not need it.
  EOT
  default     = ""
}

variable "codex_path" {
  type        = string
  description = <<-EOT
    Path to the codex CLI the "codex" agent framework runs as a
    subprocess -- agy_path's counterpart for the third framework. Empty
    resolves "codex" inside the deployment image, which carries one
    (Dockerfile), and is what an ordinary deployment wants.
  EOT
  default     = ""
}

variable "gemini_model" {
  type        = string
  description = "Model override. Empty uses the daemon's own default (pkg/agent/antigravity.DefaultModel)."
  default     = ""
}

variable "gemini_effort" {
  type        = string
  description = <<-EOT
    Reasoning effort override for the antigravity agent framework, asked
    for beside gemini_model -- low, medium or high. agy takes the two as
    separate values and refuses a pair that does not go together; a
    gemini_model whose name already carries an effort ignores this
    entirely. Empty uses the daemon's own default
    (pkg/agent/antigravity.DefaultEffort).
  EOT
  default     = ""
}

variable "claude_model" {
  type        = string
  description = "Model override for the claude agent framework. Empty uses the daemon's own default (pkg/agent/claude.DefaultModel)."
  default     = ""
}

variable "codex_model" {
  type        = string
  description = "Model override for the codex agent framework. Empty uses the daemon's own default (pkg/agent/codex.DefaultModel)."
  default     = ""
}

variable "max_agent_turns" {
  type        = number
  description = <<-EOT
    Cap on model/tool round trips in a single agent run. 0 leaves the
    framework's own default, which is 20 (pkg/agent/antigravity's
    defaultMaxTurns).

    That default is tight for real work, and exhausting it is a hard
    failure -- "exceeded max turns without a final answer" -- not a run
    that stops early with what it has. Reading a few files, writing one,
    running a test, then add/commit/push are each a turn, and anything
    exploratory spends several more before it starts.

    Raise it for a deployment doing more than one-file changes. The cost
    of a larger cap is only paid by runs that would otherwise have
    failed, since a run that finishes stops on its own.
  EOT
  default     = 0
}

variable "slots" {
  type        = string
  description = "Comma-separated concurrency slot names -- scripts/setup.sh's own GRAIN_SLOTS."
  default     = "local"
}

variable "poll_interval" {
  type        = string
  description = "How often the daemon runs a reconcile cycle."
  default     = "10s"
}

variable "secrets_repo_url" {
  type        = string
  description = <<-EOT
    Git URL of the repository this deployment's encrypted secrets file is
    backed up to (pkg/secretsrepo). The git proxy refuses this repository
    to every sandbox. files/deploy.sh passes it to scripts/setup.sh, which
    writes <data-dir>/secrets-repo.json.

    Empty (the default) keeps the ciphertext on this host's disk alone.
    The private key is never pushed to the repository and must be restored
    separately. Authentication uses the ordinary GitHub credential ladder
    push-secrets.sh seeds.
  EOT
  default     = ""
}

variable "secrets_repo_branch" {
  type        = string
  description = "Branch the encrypted secrets file lives on in secrets_repo_url (pkg/secretsrepo.DefaultBranch)."
  default     = "main"
}

# --------------------------------------------------------------- granule ---
#
# Wires the daemon's -granule-* flags (pkg/cli/daemon.go) through so
# this deployment dispatches onto real bwsalmon/granule-managed VMs
# (orchestrator.GranuleSandboxes) instead of plain host directories
# (orchestrator.HostSandboxes) -- see scripts/granule/README.md for the
# guest image, third_party/granule/VENDORED.md for the vendored source, and
# this module's own README, "Granule sandboxing", for what
# enable_granule_sandboxes (on by default below) actually costs on a first
# deploy now that scripts/setup.sh builds both images itself
# (bwsalmon/agents#531) rather than needing them built and published by
# hand first.

variable "enable_granule_sandboxes" {
  type        = bool
  description = <<-EOT
    Dispatch onto real bwsalmon/granule-managed VMs, one per run, over SSH
    (orchestrator.GranuleSandboxes) instead of one local Docker container
    per run (orchestrator.ContainerSandboxes).

    On by default, and needs no other setup: left with granule_oci_image
    at its empty default, scripts/setup.sh pulls the sandbox image this
    build of grain was built against and builds nothing at all. Set
    granule_oci_image to run a different one.

    Setting this false dispatches into local Docker containers instead:
    files/deploy.sh turns it into setup.sh's own
    GRAIN_CONTAINER_SANDBOXES=1. (It used to mean unisolated host
    directories; that backend has been removed from grain.)

    Needs enable_nested_virtualization too (on by default, and checked by
    instance.tf's own precondition) -- a granule VM is a nested
    cloud-hypervisor guest, and without /dev/kvm on the host it cannot
    boot at all.
  EOT
  default     = true
}

variable "granule_oci_image" {
  type        = string
  description = <<-EOT
    The sandbox container each task's VM runs inside -- a bwsalmon/granule
    OCI image (third_party/granule's own Dockerfile).

    Leave it empty, which is almost always right. The grain image this
    deployment runs has the reference of the sandbox built from its own
    commit stamped into it at build time, and scripts/setup.sh pulls
    that (bwsalmon/agents#645) -- so grain and its sandbox are always one
    commit's worth of each other, including after a rollback, with
    nothing to configure and nothing built on the host.

    Set it to run a different one: a mirror, a private copy, or a sandbox
    pinned apart from grain's own. It is pulled either way. Pointing it
    at an Artifact Registry repository in this project works -- the host
    service account gets project-wide roles/artifactregistry.reader when
    this is set (iam.tf's host_reads_granule_registry), though this module
    does not create the repository itself.

    Unrelated to the guest disk, which is now part of this same image
    disk. The two used to be set together or not at all; they are
    independent now.
  EOT
  default     = ""
}

variable "granule_ssh_user" {
  type        = string
  description = "Username GranuleSandboxes authenticates to each VM as -- matches the account the Granulefile creates in the guest. Only used when enable_granule_sandboxes is true."
  default     = "debian"
}

variable "granule_workspace" {
  type        = string
  description = "Working directory run_command/read_file/edit_file/write_file operate in on each granule VM -- matches granule_ssh_user's own home directory. Only used when enable_granule_sandboxes is true."
  default     = "/home/debian"
}

variable "granule_base_ip" {
  type        = string
  description = <<-EOT
    The "-ip" every granule VM is created with under NAT mode, passed
    verbatim (orchestrator.GranuleConfig.IP). 169.254.100.10 is inside
    netshim's own default bridge CIDR (169.254.100.1/24,
    internal/netshim/config.go's defaultBridgeCIDR).

    The same value goes to every VM rather than being offset per VM: under
    the docker backend each one gets its own netns-holder container to join
    with "--network container:", so they share no bridge and cannot collide
    on an address. The offset dates from the static-pod backend, where a
    pod's containers genuinely did share a namespace.

    Ignored under granule_net "flat" (the default), where the container
    runtime assigns the address. Only used when enable_granule_sandboxes is
    true.
  EOT
  default     = "169.254.100.10"
}

variable "granule_base_port" {
  type        = number
  description = "The \"-port\" every granule VM is created with under NAT mode, passed verbatim (orchestrator.GranuleConfig.Port) -- a DNAT target inside that VM's own network namespace, not a port published on the host, so the same value on every VM does not collide. Ignored under granule_net \"flat\" (the default). Only used when enable_granule_sandboxes is true."
  default     = 12000
}

# --------------------------------------------------------------------- dns --

variable "dns_managed_zone" {
  type        = string
  description = <<-EOT
    Name of an existing Cloud DNS *public* managed zone to add an A
    record to, pointed at this deployment's reserved static IP
    (dns.tf). Leave empty (the default) and dns.tf instead derives a
    name from the reserved IP itself using sslip.io, a public DNS
    service that resolves "<anything>.<a>.<b>.<c>.<d>.sslip.io" to
    a.b.c.d with no zone or registration needed on this project's part
    -- fine for a staging environment, per bwsalmon/agents#394's own "it
    doesn't matter what it is." Set this to use a real domain you
    control instead.
  EOT
  default     = ""
}

# ------------------------------------------------------------------- IAP ---

variable "iap_members" {
  type        = list(string)
  description = <<-EOT
    Identities granted roles/iap.httpsResourceAccessor on the load
    balancer's backend service (iap.tf) -- who may actually reach the
    UI once they are through Google's own sign-in, in the form IAM
    expects: "user:you@example.com", "group:staging-access@example.com",
    or "domain:example.com". Left empty (the default) grants nobody
    access -- a safe-but-useless default; set this before the first
    apply, or the environment is up but nobody can reach it.
  EOT
  default     = []
}

variable "create_iap_brand" {
  type        = bool
  description = <<-EOT
    Create the project's IAP OAuth brand and an OAuth client for it
    (iap.tf), instead of letting IAP use its own Google-managed client.

    Almost certainly leave this false, and leave iap_client_id and
    iap_client_secret unset with it: IAP uses a Google-managed OAuth
    client for browser access when no client is configured, which is why
    the IAP OAuth Admin API was deprecated in January 2025 and why a
    custom client is no longer part of ordinary setup. The provider made
    the backend service's client fields optional in 6.0; this module
    requires ~> 7.21, so the no-client path is available here.

    Two reasons not to turn this on even when you do want a custom
    client:

    1. A GCP project may have at most one brand, ever, and the API has
       no call to delete one, so this only ever works once per project.
    2. The provider itself warns that `google_iap_brand` "will no longer
       function as intended due to the deprecation of the IAP OAuth
       Admin API" -- `terraform validate` surfaces that warning
       directly.

    So for a custom client, create it by hand once (the GCP Console's
    "Google Auth Platform" page -- check current GCP guidance, this area
    of the product has moved) and pass it as iap_client_id and
    iap_client_secret below, rather than setting this.
  EOT
  default     = false
}

variable "iap_brand_support_email" {
  type        = string
  description = "Support email for the IAP OAuth consent screen. Required when create_iap_brand is true; must be a member of the project's org (or the caller's own address, for a project with no org)."
  default     = ""
}

variable "iap_brand_application_title" {
  type        = string
  description = "Application title shown on the IAP OAuth consent screen."
  default     = "grain"
}

variable "iap_client_id" {
  type        = string
  description = <<-EOT
    An existing IAP OAuth client id, for a deployment that wants its own
    client rather than the Google-managed one. Optional, and normally
    left empty -- see create_iap_brand above. Set this and
    iap_client_secret together; setting only one is the same as setting
    neither, since the backend service omits both unless both are
    present.
  EOT
  default     = ""
}

variable "iap_client_secret" {
  type        = string
  description = <<-EOT
    The secret for iap_client_id. Optional, and normally left empty --
    see create_iap_brand above. Sensitive: pass via -var or a tfvars
    file kept out of version control, not a committed one.
  EOT
  default     = ""
  sensitive   = true
}

# ---------------------------------------------------- cloud run iap proxy --

# The Cloud Run proxy (cloudrun-proxy.tf) and the grain-tunnel-gateway it
# The Cloud Run proxy and the grain-tunnel-gateway binary it
# ran are gone (grain/task-2099): expose_ui_publicly (iap.tf) and
# gcloud compute start-iap-tunnel (tunnel_command) are how to reach a
# daemon on this host. This stays declared only so a tfvars still turning
# the proxy on fails plan with a clear message, rather than being warned
# about as undeclared and quietly planning the proxy away.
variable "use_cloudrun_iap_proxy" {
  type        = bool
  description = "Removed. The Cloud Run proxy and tunnel gateway are gone; set expose_ui_publicly (iap.tf) or use tunnel_command instead. Must be false."
  default     = false

  validation {
    condition     = !var.use_cloudrun_iap_proxy
    error_message = "use_cloudrun_iap_proxy has been removed along with cloudrun-proxy.tf and grain-tunnel-gateway: reach the daemon through expose_ui_publicly (iap.tf) or gcloud compute start-iap-tunnel (tunnel_command) instead. Remove use_cloudrun_iap_proxy and the cloudrun_proxy_*, cloudrun_tunnel_* and cloudrun_connector_* settings from your tfvars."
  }
}

variable "sandbox_project_id" {
  type        = string
  description = "The project id to use for the sandbox cluster and network, creating a multi-project isolation environment."
  default     = ""
}
