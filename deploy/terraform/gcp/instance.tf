locals {
  agent_service_account_email = local.agent_account_needed ? google_service_account.agent[0].email : ""

  # Empty together with gcp_agent_service_account above whenever no
  # agent account exists at all, mirroring v1's instance.tf's
  # own gemini_project_id local: a -gcp-project daemon flag with no
  # matching -gcp-agent-service-account is a combination nothing here
  # has a reason to ever produce.
  gcp_project = local.agent_account_needed ? var.project_id : ""

  # Everything files/deploy.sh needs to run scripts/setup.sh, and
  # nothing it does not -- no secret value here, the same
  # precedent v1's instance.tf set: the GitHub PAT and the minter's
  # own key arrive separately, pushed into Secret Manager by
  # push-secrets.sh after `terraform apply` returns, never through
  # Terraform. What is here is only where to find them.
  grain_config = {
    grain_repo_url            = var.grain_repo_url
    grain_ref                 = var.grain_ref
    grain_image               = var.grain_image
    grain_image_tag           = var.grain_image_tag
    grain_image_pull_user     = var.grain_image_pull_user
    github_host               = var.github_host
    credential_name           = var.credential_name
    default_target_repo       = var.default_target_repo
    target_repos              = join(",", var.test_repos)
    ui_port                   = var.ui_port
    slots                     = var.slots
    poll_interval             = var.poll_interval
    secrets_repo_url          = var.secrets_repo_url
    secrets_repo_branch       = var.secrets_repo_branch
    agy_path                  = var.agy_path
    codex_path                = var.codex_path
    gemini_model              = var.gemini_model
    gemini_effort             = var.gemini_effort
    claude_model              = var.claude_model
    codex_model               = var.codex_model
    max_agent_turns           = var.max_agent_turns
    gcp_project               = local.gcp_project
    gcp_agent_service_account = local.agent_service_account_email

    # key -> projects/<project>/secrets/<name_prefix>-<key>, for every
    # secret in secrets.tf -- names, never values. deploy.sh reads each
    # one's latest version with the host account's own token. Iterated
    # over the read grants rather than the secrets themselves, so the
    # rollout this triggers waits for the host to be allowed to read them.
    secret_ids = { for k, _ in google_secret_manager_secret_iam_member.host_reads : k => google_secret_manager_secret.host[k].id }

    # See variables.tf's own "granule" section -- enable_granule_sandboxes on
    # with these otherwise empty (the default) is not a misconfiguration:
    # it tells scripts/setup.sh's own ensure_granule_images to build both
    # images itself rather than fetch a pair published elsewhere.
    enable_granule_sandboxes = var.enable_granule_sandboxes
    granule_oci_image        = var.granule_oci_image
    granule_ssh_user         = var.granule_ssh_user
    granule_workspace        = var.granule_workspace
    granule_base_ip          = var.granule_base_ip
    granule_base_port        = var.granule_base_port
  }
}

# Holds v2's embedded SQLite store (pkg/model/sqlite) and its secrets
# database and credential files (pkg/secrets) -- see scripts/setup.sh's
# own GRAIN_DATA_DIR default, /var/lib/grain/data, which is also where
# files/startup.sh mounts this disk, so no override is needed for the
# two to agree. This is the disk to back up (snapshot); the sandbox disk
# below never is.
#
# Separate from the boot disk specifically so the host VM can be
# recreated -- a new boot_image, a bigger machine_type, a from-scratch
# `terraform apply` after deleting the instance -- without losing any of
# that state (bwsalmon/agents#394's own "so the entire VM can be
# redeployed if needed without wiping the state").
resource "google_compute_disk" "data" {
  name   = "${var.name_prefix}-data"
  type   = var.data_disk_type
  zone   = var.zone
  size   = var.data_disk_gb
  labels = var.labels

  lifecycle {
    prevent_destroy = true
  }
}

# Everything a sandbox writes, and nothing else: docker's data root --
# the sandbox image and the qcow2 overlay each granule VM's container
# writes its root filesystem into (bwsalmon/granule#37) -- and
# grain's scratch space (HostSandboxes' per-run checkouts, the package
# and Actions caches), which files/startup.sh bind-mounts from here onto
# scripts/setup.sh's own $GRAIN_SANDBOX_DIR, /var/lib/grain/scratch. Where the
# bulk of this host's storage lives, by design.
#
# The point is not capacity but blast radius: with all of that on the
# boot disk, one task that writes a hundred gigabytes takes down the OS,
# the journal, config-sync and the daemon with it. On its own volume it
# costs the tasks running at the time and nothing else -- the host stays
# up, the deploy still runs, and the UI still says so.
#
# No prevent_destroy, unlike the data disk above: everything here is
# rebuilt by the next deploy (a re-pulled image) or belongs to a run that
# is already over.


resource "google_compute_disk" "sandbox" {
  count  = var.sandbox_disk_gb > 0 ? 1 : 0
  name   = "${var.name_prefix}-sandbox"
  type   = var.sandbox_disk_type
  zone   = var.zone
  size   = var.sandbox_disk_gb
  labels = var.labels
}

resource "google_compute_instance" "host" {
  name         = "${var.name_prefix}-host"
  machine_type = var.machine_type
  zone         = var.zone
  tags         = [local.host_tag]
  labels       = var.labels

  # Metadata updates (a new deploy_generation) apply to a running
  # instance; a machine_type change needs a stop, and this permits it.
  allow_stopping_for_update = true

  # /dev/kvm, for a deployment whose sandboxes are VMs rather than host
  # directories. Without this the device does not exist and anything
  # expecting it fails on the host, not here.
  dynamic "advanced_machine_features" {
    for_each = var.enable_nested_virtualization ? [1] : []
    content {
      enable_nested_virtualization = true
    }
  }

  boot_disk {
    initialize_params {
      image  = var.boot_image
      size   = var.boot_disk_gb
      type   = "pd-balanced"
      labels = var.labels
    }
  }

  attached_disk {
    source      = google_compute_disk.data.id
    device_name = "grain-data"
    mode        = "READ_WRITE"
  }



  # device_name is the whole contract with files/startup.sh: it is what
  # the guest's /dev/disk/by-id/google-<device_name> link is named, and
  # that link is the only stable way to tell the two attached disks
  # apart from inside the VM.
  dynamic "attached_disk" {
    for_each = google_compute_disk.sandbox
    content {
      source      = attached_disk.value.id
      device_name = "grain-sandbox"
      mode        = "READ_WRITE"
    }
  }

  network_interface {
    network    = local.network_name
    subnetwork = local.subnetwork_name

    dynamic "access_config" {
      for_each = var.assign_external_ip ? [1] : []
      content {}
    }
  }

  service_account {
    email  = google_service_account.host.email
    scopes = ["cloud-platform"]
  }

  scheduling {
    # Follows enable_nested_virtualization, because the right answer
    # differs and neither is safe as a blanket default.
    #
    # With nested guests: TERMINATE, the same choice terraform/gcp makes
    # for v1, whose comment gives the reason -- "nested virtualization
    # and live migration have a history". config-sync reconverges on
    # boot, so a terminate-and-restart costs a rollout, not state.
    #
    # Without them: MIGRATE, so the daemon rides out a host maintenance
    # event instead of being killed. This also matters for what can run
    # here at all -- E2 rejects TERMINATE unless the instance is spot,
    # so hardcoding TERMINATE made an E2 machine_type impossible, which
    # is what "e2 instances do not support maintenance terminate unless
    # spot" meant on a first apply.
    on_host_maintenance = var.enable_nested_virtualization ? "TERMINATE" : "MIGRATE"
    automatic_restart   = true
  }

  dynamic "shielded_instance_config" {
    for_each = var.enable_shielded_vm ? [1] : []
    content {
      enable_secure_boot          = true
      enable_vtpm                 = true
      enable_integrity_monitoring = true
    }
  }
  metadata = {
    enable-oslogin          = var.enable_os_login ? "TRUE" : "FALSE"
    enable-guest-attributes = "TRUE"

    startup-script = file("${path.module}/files/startup.sh")

    # Shipped as metadata, not fetched from this repo at boot, so the
    # host needs no credential for grain even if it were private --
    # mirrors v1's instance.tf's exact reasoning.
    grain-config-sync-script = file("${path.module}/files/config-sync.sh")
    grain-deploy-script      = file("${path.module}/files/deploy.sh")

    grain-config = jsonencode(local.grain_config)

    # Whether files/startup.sh should expect a second disk at all. Read
    # there rather than inferred from the device showing up, so a disk
    # that was declared and then failed to attach is a warning in the
    # boot log instead of a deployment that silently fills its boot disk
    # again -- and so that turning sandbox_disk_gb back to 0 tells the
    # host to undo the docker configuration that goes with the volume.
    # Not part of grain-config: nothing inside the container needs it,
    # and it is not worth a rollout of its own.
    grain-sandbox-disk = var.sandbox_disk_gb > 0 ? "true" : "false"

    # Changing this is what triggers a rollout -- see variables.tf's
    # deploy_generation. Folded together with a hash of grain_config
    # itself, the same fix v1's instance.tf's own copy of this
    # line got for bwsalmon/agents#592: without it, a manual `terraform
    # apply` (deploy_generation's own "manual" default) that only edits a
    # grain_config value never rolls out, because config-sync's whole
    # trigger is this one field and nothing rechecks grain_config on its
    # own -- not on the next tick, not after rebooting the host.
    grain-deploy-generation = "${var.deploy_generation}-${substr(sha256(jsonencode(local.grain_config)), 0, 12)}"
  }

  lifecycle {
    # E2 supports neither nested virtualization nor the TERMINATE
    # maintenance policy it forces, and GCP reports the second failure
    # first -- "e2 instances do not support maintenance terminate unless
    # spot" -- which says nothing about nested virtualization at all.
    # Caught here so the message names the actual constraint.
    precondition {
      condition     = !var.enable_nested_virtualization || !startswith(var.machine_type, "e2-")
      error_message = "enable_nested_virtualization needs a machine family that supports it -- N1, N2, N2D, C2, C3, C4 or M-series. E2 does not. Either pick a non-E2 machine_type, or set enable_nested_virtualization = false if this deployment's sandboxes are host directories."
    }

    precondition {
      condition     = !(var.enable_gemini_key || var.agent_can_manage_compute_instances || var.agent_can_manage_gke || var.agent_can_manage_cloud_sql) || var.deployer_member != ""
      error_message = "enable_gemini_key, agent_can_manage_compute_instances, agent_can_manage_gke, and agent_can_manage_cloud_sql all need a real key on the agent account to do anything -- set deployer_member so push-secrets.sh can mint one after apply (see iam.tf's deployer_manages_minter_keys)."
    }

    # A guest image and an OCI image used to have to exist somewhere for
    # setup.sh to fetch before it could bring up a single granule VM, and
    # neither had a project-independent default this module could supply
    # -- so this precondition used to fail loudly here rather than
    # applying a host that could never actually create one. That is no
    # longer true (bwsalmon/agents#531, #645): left at its empty
    # default, scripts/setup.sh's own ensure_granule_images *pulls* the
    # sandbox image -- the one the grain image it is deploying was built
    # against, stamped in at build time, so nothing names it here -- and
    # builds nothing at all. The guest travels inside that same image, so
    # there is no second artifact to publish or fetch. See this module's
    # README, "Granule sandboxing". granule_oci_image overrides which image
    # is pulled, and is optional.

    # A granule VM is a nested cloud-hypervisor guest -- no /dev/kvm, no
    # boot, regardless of anything else here.
    precondition {
      condition     = !var.enable_granule_sandboxes || var.enable_nested_virtualization
      error_message = "enable_granule_sandboxes needs enable_nested_virtualization (for /dev/kvm) -- set both, or turn enable_granule_sandboxes off."
    }

    # No ignore_changes for secrets any more: none lives in this
    # instance's metadata (secrets.tf), so there is nothing for an apply
    # to mistake for drift -- and the first apply after a deployment moves
    # off metadata is what removes any attribute still left behind there
    # (deploy/migrate-secrets.sh, which terraform-apply.sh runs before it,
    # has normally already copied them into Secret Manager and removed
    # them). That removal is an in-place diff that prints the old value,
    # which is why deploy/terraform-apply.sh's redact still names every
    # one of those attribute names.
  }
}
