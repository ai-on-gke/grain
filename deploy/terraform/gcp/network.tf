# One VPC, one subnet, two firewall rules. Unlike v1's network.tf's
# v1 host, this one is not reachable on any port from the open internet at
# all: SSH is IAP-tunnel-only (same as v1), and the UI is reachable only
# from Google's own load-balancing/health-check ranges, since the actual
# public entry point is the IAP-protected HTTPS load balancer in iap.tf,
# not the instance itself.

locals {
  network_name    = var.create_network ? google_compute_network.this[0].name : var.network_name
  subnetwork_name = var.create_network ? google_compute_subnetwork.this[0].name : var.subnetwork_name
  host_tag        = "${var.name_prefix}-host"

  # The tag a task's own VMs carry, so agent_iap_ssh below reaches them
  # and nothing else -- see that rule, and this module's README,
  # "Creating a VM as the agent."
  agent_vm_tag = "${var.name_prefix}-agent-vm"

  # IAP's TCP forwarding range -- where a `gcloud compute start-iap-tunnel`
  # connection arrives from. Fixed by Google, and deliberately not
  # ssh_source_ranges: an operator may widen that to their own CIDR for
  # direct SSH, and doing so must not also open the UI port to it.
  iap_tunnel_range = "35.235.240.0/20"
}

resource "google_compute_network" "this" {
  count                   = var.create_network ? 1 : 0
  name                    = "${var.name_prefix}-net"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "this" {
  count                    = var.create_network ? 1 : 0
  name                     = "${var.name_prefix}-subnet"
  region                   = var.region
  network                  = google_compute_network.this[0].id
  ip_cidr_range            = var.subnet_cidr
  private_ip_google_access = true
}

resource "google_compute_firewall" "ssh" {
  count         = var.create_network && length(var.ssh_source_ranges) > 0 ? 1 : 0
  name          = "${var.name_prefix}-allow-ssh"
  network       = google_compute_network.this[0].name
  direction     = "INGRESS"
  source_ranges = var.ssh_source_ranges
  target_tags   = [local.host_tag]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

# The same IAP-tunnelled SSH, for the VMs a task creates rather than this
# deployment's own host. Every rule in this file is target_tags-scoped to
# host_tag, which is correct for each of them and left
# agent_can_manage_compute_instances granting a capability that stopped
# working halfway through: the agent can create an instance in this VPC
# and holds roles/iap.tunnelResourceAccessor to tunnel to it, but no rule
# admits 35.235.240.0/20 to port 22 on anything but the host, so
# `gcloud compute ssh --tunnel-through-iap` hangs and then times out --
# and instanceAdmin.v1 can read firewall rules but not create them, so
# the agent cannot open the path itself either.
#
# Scoped to its own tag rather than the whole network so this does not
# silently become a second way in to the host (or to any unrelated
# instance an operator runs here), and so an instance created without the
# tag stays unreachable by default. Reaching through this range at all
# still requires roles/iap.tunnelResourceAccessor, exactly as
# tunnel_to_ui's own comment argues: the range is not the control, the
# IAM grant is.
resource "google_compute_firewall" "agent_iap_ssh" {
  count         = var.create_network && var.agent_can_manage_compute_instances ? 1 : 0
  name          = "${var.name_prefix}-allow-agent-iap-ssh"
  network       = google_compute_network.this[0].name
  direction     = "INGRESS"
  source_ranges = [local.iap_tunnel_range]
  target_tags   = [local.agent_vm_tag]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

# 130.211.0.0/22 and 35.191.0.0/16 are Google's own health-check and
# load-balancer-to-backend source ranges for a global external HTTPS
# load balancer (iap.tf's google_compute_backend_service) -- not the
# public internet, which is exactly what lets ui_port bind 0.0.0.0
# safely (variables.tf's own comment on ui_port). No source range here
# is ever widened by an operator the way ssh_source_ranges might be:
# the load balancer, gated by IAP, is the only intended path to this
# port.
resource "google_compute_firewall" "lb_to_ui" {
  count         = var.create_network && var.expose_ui_publicly ? 1 : 0
  name          = "${var.name_prefix}-allow-lb-to-ui"
  network       = google_compute_network.this[0].name
  direction     = "INGRESS"
  source_ranges = ["130.211.0.0/22", "35.191.0.0/16"]
  target_tags   = [local.host_tag]

  allow {
    protocol = "tcp"
    ports    = [tostring(var.ui_port)]
  }
}

# ui_port over IAP's TCP tunnel, which is the whole access path when
# expose_ui_publicly is off and a debugging one when it is on. Reaching
# through this range at all requires roles/iap.tunnelResourceAccessor, so
# it is authenticated the same way the SSH rule above is -- the range is
# not the control, the IAM grant is.
resource "google_compute_firewall" "tunnel_to_ui" {
  count         = var.create_network ? 1 : 0
  name          = "${var.name_prefix}-allow-tunnel-to-ui"
  network       = google_compute_network.this[0].name
  direction     = "INGRESS"
  source_ranges = [local.iap_tunnel_range]
  target_tags   = [local.host_tag]

  allow {
    protocol = "tcp"
    ports    = [tostring(var.ui_port)]
  }
}

# Egress is open: the host fetches Debian packages, Docker images, the
# grain source, and talks to the GitHub and Gemini APIs.

resource "google_compute_router" "nat" {
  count   = var.create_network && var.enable_cloud_nat ? 1 : 0
  name    = "${var.name_prefix}-router"
  region  = var.region
  network = google_compute_network.this[0].id
}

resource "google_compute_router_nat" "nat" {
  count                              = var.create_network && var.enable_cloud_nat ? 1 : 0
  name                               = "${var.name_prefix}-nat"
  region                             = var.region
  router                             = google_compute_router.nat[0].name
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"

  log_config {
    enable = true
    filter = "ERRORS_ONLY"
  }
}
