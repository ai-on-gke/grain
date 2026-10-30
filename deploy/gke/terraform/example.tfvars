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

project_id                 = "my-gcp-project"
region                     = "us-central1"
zone                       = "us-central1-a"
cluster_name               = "grain-gke"
adopt_existing_cluster     = false
namespace                  = "grain"
daemon_machine_type        = "e2-standard-4"
sandbox_machine_type       = "n2-standard-16"
sandbox_min_nodes          = 1
sandbox_max_nodes          = 8
postgres_mode              = "cloudsql"
cloudsql_instance_name     = "grain-postgres"
cloudsql_tier              = "db-custom-2-8192"
cloudsql_availability_type = "REGIONAL"
domain                     = "grain.example.com"
dns_managed_zone           = ""
iap_allowed_members        = ["user:operator@example.com"]
# Leave unset to use the Google-managed IAP OAuth client. Set both only for a
# custom client created in the console (Google Auth Platform -> Clients).
# iap_client_id     = "CHANGE-ME.apps.googleusercontent.com"
# iap_client_secret = "CHANGE-ME"
