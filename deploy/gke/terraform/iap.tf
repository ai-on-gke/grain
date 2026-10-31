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

# Gateway static IP, managed TLS certificate, Cloud DNS A record, and IAP OAuth
# configuration for Grain on GKE.
#
# The GKE Gateway controller creates the underlying global external Application
# Load Balancer and backend services for Service/grain, Service/grain-hostd, and
# Service/grain-webhook from deploy/kubernetes/gke/gateway.yaml, binding the
# named IP (`grain-gateway-ip`) and pre-shared managed certificate
# (`grain-gateway-cert`) created below.

resource "google_compute_global_address" "gateway_ip" {
  name    = "grain-gateway-ip"
  project = var.project_id

  depends_on = [google_project_service.services]
}

resource "google_compute_managed_ssl_certificate" "gateway_cert" {
  name    = "grain-gateway-cert"
  project = var.project_id

  managed {
    domains = [var.domain]
  }

  depends_on = [google_project_service.services]
}

resource "google_dns_record_set" "gateway" {
  count        = var.dns_managed_zone != "" ? 1 : 0
  name         = "${trimsuffix(var.domain, ".")}."
  project      = var.project_id
  managed_zone = var.dns_managed_zone
  type         = "A"
  ttl          = 300
  rrdatas      = [google_compute_global_address.gateway_ip.address]
}

# No google_iap_brand / google_iap_client here: Google shut the IAP OAuth
# Admin API they wrap down on 2026-03-19. IAP uses a Google-managed OAuth
# client by default; pass iap_client_id/iap_client_secret for a client of
# your own, created in the console (Google Auth Platform -> Clients).

locals {
  iap_client_id = var.iap_client_id

  iap_backend_member_bindings = {
    for pair in setproduct(toset(var.iap_backend_services), toset(var.iap_allowed_members)) :
    "${pair[0]}:${pair[1]}" => {
      backend_service = pair[0]
      member          = pair[1]
    }
  }
}

# Scoped strictly to the GKE Gateway's synthesized `grain` and `grain-hostd`
# backend services (never project-wide and never onto `grain-webhook`).
resource "google_iap_web_backend_service_iam_member" "allowed_members" {
  for_each            = local.iap_backend_member_bindings
  project             = var.project_id
  web_backend_service = each.value.backend_service
  role                = "roles/iap.httpsResourceAccessor"
  member              = each.value.member

  depends_on = [google_project_service.services]
}
