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

output "instance_name" {
  value       = google_compute_instance.host.name
  description = "Host VM name, for gcloud commands."
}

output "zone" {
  value       = var.zone
  description = "Zone the host runs in."
}

output "project_id" {
  value       = var.project_id
  description = "Convenience for scripts that read the project from state rather than re-parsing tfvars."
}

# The three below are null when expose_ui_publicly is off: there is no
# load balancer, so there is no address, no name and no URL. Read them
# null-safe (`terraform output -raw url 2>/dev/null || true`) -- -raw
# errors on a null rather than printing nothing.
output "url" {
  value       = var.expose_ui_publicly ? "https://${local.dns_name}/" : null
  description = "The staging environment's fixed URL -- reachable once signed in as one of iap_members. Null when expose_ui_publicly is off; use tunnel_command instead."
}

output "dns_name" {
  value       = var.expose_ui_publicly ? local.dns_name : null
  description = "The fixed DNS name itself, with no scheme -- what the managed SSL certificate is issued for. Null when expose_ui_publicly is off."
}

output "load_balancer_ip" {
  value       = var.expose_ui_publicly ? google_compute_global_address.lb[0].address : null
  description = "The reserved static IP the DNS name resolves to. Stable across a redeploy of the host VM. Null when expose_ui_publicly is off."
}

output "tunnel_command" {
  value       = "gcloud compute start-iap-tunnel ${google_compute_instance.host.name} ${var.ui_port} --local-host-port=localhost:${var.ui_port} --zone ${var.zone} --project ${var.project_id}"
  description = "Forward the UI to localhost over IAP's TCP tunnel, then open http://localhost:<ui_port>. The whole access path when expose_ui_publicly is off, and a way in past a broken load balancer when it is on."
}

output "host_service_account" {
  value       = google_service_account.host.email
  description = "The identity the host VM runs as."
}

output "agent_service_account" {
  value       = local.agent_account_needed ? google_service_account.agent[0].email : null
  description = "The narrow identity pkg/capability/gcpkey mints per-task keys for, when configured."
}

output "minter_service_account" {
  value       = local.agent_account_needed ? google_service_account.minter[0].email : null
  description = "The identity push-secrets.sh mints a key for, to seed the host with -- see that script."
}

# The flags a task's own `gcloud compute instances create` needs for the
# instance to land somewhere iam.tf's grants and network.tf's
# agent_iap_ssh rule actually cover -- a bare `create` with none of them
# fails on the default Compute Engine service account, and an instance
# created without --tags is unreachable over IAP once it exists. See this
# module's README, "Creating a VM as the agent." Null when
# agent_can_manage_compute_instances is off, since then there is no agent
# account and no rule for these to refer to.
#
# --no-address assumes enable_cloud_nat (the default) for the instance's
# own egress, and the tag only means anything when create_network is on;
# with an operator-supplied network, opening 35.235.240.0/20 to port 22
# for that tag is theirs to do.
output "agent_vm_create_flags" {
  value = var.agent_can_manage_compute_instances ? join(" ", [
    "--project=${var.project_id}",
    "--zone=${var.zone}",
    "--network=${local.network_name}",
    "--subnet=${local.subnetwork_name}",
    "--no-address",
    "--tags=${local.agent_vm_tag}",
    "--service-account=${google_service_account.agent[0].email}",
    "--scopes=cloud-platform",
  ]) : null
  description = "Flags for a task creating its own VM, so that IAM permits the create and IAP can reach port 22 afterwards. Null when agent_can_manage_compute_instances is off."
}

output "ssh_command" {
  value       = "gcloud compute ssh ${google_compute_instance.host.name} --zone ${var.zone} --project ${var.project_id} --tunnel-through-iap"
  description = "How to get a shell on the host despite it having no external IP and no open SSH port."
}
