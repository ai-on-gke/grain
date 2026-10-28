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

# Exposes the staging UI at dns.tf's local.dns_name over HTTPS, protected
# by Identity-Aware Proxy -- bwsalmon/agents#394's own "protected by iap."
#
# IAP for a web UI (as opposed to the IAP *TCP* tunnel ssh_source_ranges
# already gives SSH, in network.tf) is a property of a global external
# Application Load Balancer's backend service, not of the instance
# itself: Google's edge terminates TLS, requires a Google sign-in and an
# roles/iap.httpsResourceAccessor grant before a request ever reaches the
# backend, and only then forwards it, over plain HTTP, to the instance's
# *internal* IP -- which is why google_compute_instance.host needs no
# external IP of its own (network.tf's assign_external_ip) and why
# ui_port only has to be reachable from Google's own load-balancer
# ranges (network.tf's lb_to_ui firewall rule), not the internet.
#
#   browser ──HTTPS, Google sign-in──▶ IAP (on the backend service)
#                                           │ authorized (iap_members)
#                                           ▼
#                                   plain HTTP, internal IP
#                                           │
#                                           ▼
#                              google_compute_instance.host:ui_port

resource "google_compute_health_check" "ui" {
  count = var.expose_ui_publicly ? 1 : 0
  name  = "${var.name_prefix}-ui-health"

  http_health_check {
    port         = var.ui_port
    request_path = "/"
  }
}

# Unmanaged: one fixed instance, not a template a group would scale --
# this deployment is one VM, on purpose (variables.tf's own machine_type
# comment on why v2 needs no fleet).
resource "google_compute_instance_group" "host" {
  count     = var.expose_ui_publicly ? 1 : 0
  name      = "${var.name_prefix}-ig"
  zone      = var.zone
  instances = [google_compute_instance.host.self_link]

  named_port {
    name = "http"
    port = var.ui_port
  }
}

resource "google_compute_backend_service" "ui" {
  count                 = var.expose_ui_publicly ? 1 : 0
  name                  = "${var.name_prefix}-ui-backend"
  protocol              = "HTTP"
  port_name             = "http"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  health_checks         = [google_compute_health_check.ui[0].id]

  backend {
    group = google_compute_instance_group.host[0].self_link
  }

  iap {
    enabled = true

    # Omitted -- null, not "" -- when no custom client is configured,
    # which is what makes IAP use its own Google-managed OAuth client
    # instead. That is the default for browser access, and the reason
    # the IAP OAuth Admin API (and with it google_iap_brand) was
    # deprecated in January 2025: there is normally no client to create
    # any more. The provider made these two optional in 6.0; this module
    # requires ~> 7.21.
    #
    # So the common path needs no OAuth client at all, and none of the
    # console steps that used to come with one. Set iap_client_id and
    # iap_client_secret only for a client of your own -- see
    # variables.tf's own create_iap_brand for when that is still worth
    # it.
    oauth2_client_id     = local.iap_client_id != "" ? local.iap_client_id : null
    oauth2_client_secret = local.iap_client_secret != "" ? local.iap_client_secret : null
  }

  depends_on = [google_project_service.iap]
}

resource "google_compute_url_map" "ui" {
  count           = var.expose_ui_publicly ? 1 : 0
  name            = "${var.name_prefix}-ui-map"
  default_service = google_compute_backend_service.ui[0].id
}

resource "google_compute_managed_ssl_certificate" "ui" {
  count = var.expose_ui_publicly ? 1 : 0
  name  = "${var.name_prefix}-ui-cert"

  managed {
    domains = [local.dns_name]
  }
}

resource "google_compute_target_https_proxy" "ui" {
  count            = var.expose_ui_publicly ? 1 : 0
  name             = "${var.name_prefix}-ui-proxy"
  url_map          = google_compute_url_map.ui[0].id
  ssl_certificates = [google_compute_managed_ssl_certificate.ui[0].id]
}

resource "google_compute_global_forwarding_rule" "ui" {
  count                 = var.expose_ui_publicly ? 1 : 0
  name                  = "${var.name_prefix}-ui-fr"
  ip_address            = google_compute_global_address.lb[0].address
  ip_protocol           = "TCP"
  port_range            = "443"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  target                = google_compute_target_https_proxy.ui[0].id
}

# --- an OAuth brand/client, for a deployment that wants its own -------
#
# Not needed at all in the normal case: with neither this nor
# iap_client_id/iap_client_secret set, the backend service above omits
# the client fields and IAP uses its own Google-managed one.
#
# Off by default, and doubly so -- a project may have at most one brand,
# ever, and the provider itself warns this resource no longer functions
# as intended for a genuinely new brand, following the IAP OAuth Admin
# API's deprecation. Kept for a project that already has a brand, or one
# that needs a custom client for something a Google-managed client does
# not cover.

resource "google_iap_brand" "this" {
  count             = var.expose_ui_publicly && var.create_iap_brand ? 1 : 0
  support_email     = var.iap_brand_support_email
  application_title = var.iap_brand_application_title
  project           = var.project_id

  depends_on = [google_project_service.iap]

  lifecycle {
    precondition {
      condition     = var.iap_brand_support_email != ""
      error_message = "iap_brand_support_email is required when create_iap_brand is true."
    }
  }
}

resource "google_iap_client" "this" {
  count        = var.expose_ui_publicly && var.create_iap_brand ? 1 : 0
  brand        = google_iap_brand.this[0].name
  display_name = "${var.name_prefix} UI"
}

locals {
  iap_client_id     = var.expose_ui_publicly && var.create_iap_brand ? google_iap_client.this[0].client_id : var.iap_client_id
  iap_client_secret = var.expose_ui_publicly && var.create_iap_brand ? google_iap_client.this[0].secret : var.iap_client_secret
}

# Who may actually reach the UI once they are through Google's own sign-in
# -- see variables.tf's iap_members. Scoped to this one backend service,
# not the whole project, so a broader roles/iap.httpsResourceAccessor
# grant elsewhere (another IAP-protected app in the same project) does
# not also open this one.
resource "google_iap_web_backend_service_iam_member" "members" {
  for_each            = var.expose_ui_publicly ? toset(var.iap_members) : toset([])
  project             = var.project_id
  web_backend_service = google_compute_backend_service.ui[0].name
  role                = "roles/iap.httpsResourceAccessor"
  member              = each.value
}

resource "google_project_service" "iap" {
  project            = var.project_id
  service            = "iap.googleapis.com"
  disable_on_destroy = false
}
