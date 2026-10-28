# A fixed DNS name for the staging environment (bwsalmon/agents#394's own
# "doesn't matter what it is"), stable across every future `terraform
# apply` -- including one that recreates google_compute_instance.host --
# because it names the reserved static IP (below), a resource entirely
# separate from the instance itself.

# Global, not regional: the load balancer this IP fronts (iap.tf) is a
# global external Application Load Balancer.
resource "google_compute_global_address" "lb" {
  count = var.expose_ui_publicly ? 1 : 0
  name  = "${var.name_prefix}-lb-ip"
}

locals {
  # sslip.io resolves "<anything>.<a>-<b>-<c>-<d>.sslip.io" (dashes, not
  # dots, in the IP octets -- dots there would make it a different,
  # invalid label) to a.b.c.d, so this name is well-defined the moment
  # the address above is reserved, needs no DNS zone this project has to
  # own, and never changes as long as that address is not destroyed --
  # see variables.tf's dns_managed_zone for using a real domain instead.
  sslip_dns_name = var.expose_ui_publicly ? "${var.name_prefix}-${replace(google_compute_global_address.lb[0].address, ".", "-")}.sslip.io" : ""

  # Empty when nothing public exists to name -- every consumer below and
  # in outputs.tf is guarded on expose_ui_publicly rather than on this
  # being non-empty, so an empty name is never rendered into a URL.
  dns_name = !var.expose_ui_publicly ? "" : (
    var.dns_managed_zone != "" ? trimsuffix("${var.name_prefix}.${data.google_dns_managed_zone.this[0].dns_name}", ".") : local.sslip_dns_name
  )
}

data "google_dns_managed_zone" "this" {
  count = var.expose_ui_publicly && var.dns_managed_zone != "" ? 1 : 0
  name  = var.dns_managed_zone
}

resource "google_dns_record_set" "host" {
  count        = var.expose_ui_publicly && var.dns_managed_zone != "" ? 1 : 0
  name         = "${var.name_prefix}.${data.google_dns_managed_zone.this[0].dns_name}"
  managed_zone = var.dns_managed_zone
  type         = "A"
  ttl          = 300
  rrdatas      = [google_compute_global_address.lb[0].address]
}
