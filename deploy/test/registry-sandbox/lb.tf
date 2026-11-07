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

# Global static IPv4 address for the External Application Load Balancer.
resource "google_compute_global_address" "lb" {
  name       = "${var.name_prefix}-lb-ip"
  depends_on = [google_project_service.enabled["compute.googleapis.com"]]
}

locals {
  ip_dashed = replace(google_compute_global_address.lb.address, ".", "-")

  # `host` and `instance_domain` must be sibling domains (not parent/child) so
  # `host` never collides with single-label `<entry>.<instance_domain>` routing.
  # Both resolve to `google_compute_global_address.lb.address` via sslip.io.
  registry_host   = var.registry_host != "" ? lower(trimspace(var.registry_host)) : "${var.name_prefix}-${local.ip_dashed}.sslip.io"
  instance_domain = var.instance_domain != "" ? lower(trimspace(var.instance_domain)) : "i-${var.name_prefix}-${local.ip_dashed}.sslip.io"

  use_managed_cert    = contains(["hybrid", "managed"], var.tls_mode)
  use_selfsigned_cert = contains(["hybrid", "self-signed"], var.tls_mode)

  managed_cert_domains = distinct(concat(
    [local.registry_host],
    [for e in var.managed_entry_names : "${lower(trimspace(e))}.${local.instance_domain}"]
  ))

  selfsigned_dns_names = var.tls_mode == "self-signed" ? [
    local.registry_host,
    "*.${local.instance_domain}",
  ] : ["*.${local.instance_domain}"]

  human_emails = distinct([
    for e in concat([var.admin_email], var.extra_users) : lower(trimspace(e))
    if trimspace(e) != ""
  ])

  iap_members = distinct(concat(
    [for e in local.human_emails : "user:${e}"],
    local.effective_verifier_sa != "" ? ["serviceAccount:${local.effective_verifier_sa}"] : []
  ))

  iap_audience = "/projects/${data.google_project.this.number}/global/backendServices/${google_compute_backend_service.iap.generated_id}"
}

resource "google_compute_health_check" "iap" {
  name = "${var.name_prefix}-hc-8080"

  http_health_check {
    port         = 8080
    host         = local.registry_host
    request_path = "/healthz"
  }
}

resource "google_compute_health_check" "public" {
  name = "${var.name_prefix}-hc-8081"

  http_health_check {
    port         = 8081
    host         = local.registry_host
    request_path = "/healthz"
  }
}

# Unmanaged single-VM instance group; membership is attached via
# `google_compute_instance_group_membership` in `instance.tf` so the VM can
# read `google_compute_backend_service.iap.generated_id` without a cycle.
resource "google_compute_instance_group" "registry" {
  name    = "${var.name_prefix}-ig"
  zone    = var.zone
  network = google_compute_network.this.self_link

  named_port {
    name = "http-iap"
    port = 8080
  }

  named_port {
    name = "http-public"
    port = 8081
  }

  lifecycle {
    create_before_destroy = true
  }
}

# IAP-protected backend service forwarding human/API requests and routed
# `<entry>.<instance_domain>` traffic to grain-registry's `:8080` listener.
resource "google_compute_backend_service" "iap" {
  name                  = "${var.name_prefix}-iap-bs"
  protocol              = "HTTP"
  port_name             = "http-iap"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  timeout_sec           = 86400
  health_checks         = [google_compute_health_check.iap.id]

  backend {
    group = google_compute_instance_group.registry.self_link
  }

  # Created with the Google-managed OAuth client, which only admits users in
  # the project's own organization. configure-oauth.sh can switch it to a
  # custom OAuth client (Console-created; its secret never enters this repo
  # or Terraform state), and Terraform leaves that choice alone.
  iap {
    enabled              = true
    oauth2_client_id     = null
    oauth2_client_secret = null
  }

  lifecycle {
    ignore_changes = [
      iap[0].oauth2_client_id,
      iap[0].oauth2_client_secret,
    ]
  }

  depends_on = [google_project_service.enabled["iap.googleapis.com"]]
}

resource "google_iap_web_backend_service_iam_member" "admitted" {
  for_each            = toset(local.iap_members)
  project             = var.project_id
  web_backend_service = google_compute_backend_service.iap.name
  role                = "roles/iap.httpsResourceAccessor"
  member              = each.value
}

# Public backend service (no IAP) forwarding strictly `/.well-known/jwks.json`,
# `/healthz`, and `grc_...` token-authenticated heartbeat/tunnel-connect calls
# to grain-registry's locked-down `PublicHandler` on `:8081`.
resource "google_compute_backend_service" "public" {
  name                  = "${var.name_prefix}-pub-bs"
  protocol              = "HTTP"
  port_name             = "http-public"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  timeout_sec           = 86400
  health_checks         = [google_compute_health_check.public.id]

  backend {
    group = google_compute_instance_group.registry.self_link
  }
}

resource "google_compute_url_map" "registry" {
  name            = "${var.name_prefix}-url-map"
  default_service = google_compute_backend_service.iap.id

  host_rule {
    hosts        = [local.registry_host]
    path_matcher = "registry-host"
  }

  path_matcher {
    name            = "registry-host"
    default_service = google_compute_backend_service.iap.id

    route_rules {
      priority = 1
      service  = google_compute_backend_service.public.id

      match_rules {
        full_path_match = "/.well-known/jwks.json"
      }
      match_rules {
        full_path_match = "/healthz"
      }
    }

    route_rules {
      priority = 2
      service  = google_compute_backend_service.public.id

      match_rules {
        prefix_match = "/api/entries/"
        header_matches {
          header_name = "x-grain-auth"
          exact_match = "token"
        }
      }
    }
  }
}

resource "google_compute_managed_ssl_certificate" "primary" {
  count = local.use_managed_cert ? 1 : 0
  name  = "${var.name_prefix}-mcert"

  managed {
    domains = local.managed_cert_domains
  }

  lifecycle {
    create_before_destroy = true

    precondition {
      condition     = alltrue([for d in local.managed_cert_domains : length(d) <= 63])
      error_message = "Every domain on google_compute_managed_ssl_certificate must be <= 63 characters; shorten name_prefix or managed_entry_names, or pass a shorter custom domain."
    }
  }
}

resource "tls_private_key" "wildcard" {
  count       = local.use_selfsigned_cert ? 1 : 0
  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

resource "tls_self_signed_cert" "wildcard" {
  count           = local.use_selfsigned_cert ? 1 : 0
  private_key_pem = tls_private_key.wildcard[0].private_key_pem

  subject {
    common_name  = local.selfsigned_dns_names[0]
    organization = "Grain Registry Sandbox Test"
  }

  dns_names             = local.selfsigned_dns_names
  validity_period_hours = 2160 # 90 days

  allowed_uses = [
    "key_encipherment",
    "digital_signature",
    "server_auth",
  ]
}

resource "google_compute_ssl_certificate" "wildcard" {
  count       = local.use_selfsigned_cert ? 1 : 0
  name_prefix = "${var.name_prefix}-wcert-"
  private_key = tls_private_key.wildcard[0].private_key_pem
  certificate = tls_self_signed_cert.wildcard[0].cert_pem

  lifecycle {
    create_before_destroy = true
  }
}

locals {
  ssl_certificate_ids = compact(concat(
    local.use_managed_cert ? [google_compute_managed_ssl_certificate.primary[0].id] : [],
    local.use_selfsigned_cert ? [google_compute_ssl_certificate.wildcard[0].id] : []
  ))
}

resource "google_compute_target_https_proxy" "registry" {
  name             = "${var.name_prefix}-https-proxy"
  url_map          = google_compute_url_map.registry.id
  ssl_certificates = local.ssl_certificate_ids
}

resource "google_compute_global_forwarding_rule" "https" {
  name                  = "${var.name_prefix}-https-fr"
  ip_address            = google_compute_global_address.lb.address
  ip_protocol           = "TCP"
  port_range            = "443"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  target                = google_compute_target_https_proxy.registry.id
}
