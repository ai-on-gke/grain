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

output "registry_url" {
  description = "HTTPS base URL of the test grain-registry control plane."
  value       = "https://${local.registry_host}"
}

output "registry_host" {
  description = "DNS hostname of the test grain-registry control plane."
  value       = local.registry_host
}

output "instance_domain" {
  description = "Wildcard routing suffix (<entry>.<instance_domain>) for connected daemons."
  value       = local.instance_domain
}

output "jwks_url" {
  description = "Public JWKS endpoint consumed by connected daemons (-ui-registry-keys-url)."
  value       = "https://${local.registry_host}/.well-known/jwks.json"
}

output "lb_ip" {
  description = "Reserved global static IPv4 address of the HTTPS Load Balancer."
  value       = google_compute_global_address.lb.address
}

output "iap_audience" {
  description = "Expected Cloud IAP JWT audience configured in registry.yaml."
  value       = local.iap_audience
}

output "kms_key_name" {
  description = "Cloud KMS CryptoKey resource ID used by grain-registry for ES256 assertions."
  value       = local.kms_key_id
}

output "vm_name" {
  description = "Name of the single GCE VM hosting PostgreSQL and grain-registry."
  value       = google_compute_instance.registry.name
}

output "zone" {
  description = "GCE zone hosting the test registry VM."
  value       = var.zone
}

output "tls_mode" {
  description = "Configured TLS certificate mode on the HTTPS Load Balancer."
  value       = var.tls_mode
}

output "managed_cert_name" {
  description = "Name of the Google-managed SSL certificate (empty when tls_mode is self-signed)."
  value       = local.use_managed_cert ? google_compute_managed_ssl_certificate.primary[0].name : ""
}

output "verifier_service_account" {
  description = "Service account email admitted to IAP and registry service_principals for verify.sh."
  value       = local.effective_verifier_sa
}

output "selfsigned_cert_pem" {
  description = "PEM of the self-signed wildcard certificate on the HTTPS Load Balancer (public certificate only; empty when tls_mode is managed). verify.sh trusts it instead of disabling TLS verification."
  value       = local.use_selfsigned_cert ? tls_self_signed_cert.wildcard[0].cert_pem : ""
}

output "sql_vm_name" {
  description = "Name of the GCE VM running the shared PostgreSQL pool for hosted Grain instances."
  value       = google_compute_instance.instance_sql.name
}

output "sql_private_ip" {
  description = "Reserved private VPC IPv4 address of the shared PostgreSQL pool for hosted Grain instances."
  value       = google_compute_address.instance_sql.address
}

output "hosted_project_id" {
  description = "Project where hosted GCE VM instances (kind = instance) are created."
  value       = local.hosted_project_id
}

output "hosted_network" {
  description = "VPC network and subnetwork used by hosted GCE VM instances."
  value       = "${local.hosted_network_name}/${local.hosted_subnet_name} (${local.hosted_subnet_cidr})"
}
