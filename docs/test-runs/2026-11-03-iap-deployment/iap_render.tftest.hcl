# Plan-only, fully mocked: no Google API is called and nothing is applied.
mock_provider "google" {
  override_during = plan

  mock_data "google_project" {
    defaults = { number = "123456789012" }
  }
}

mock_provider "google" {
  alias           = "sandbox"
  override_during = plan
}

override_resource {
  target          = google_compute_backend_service.ui
  override_during = plan
  values          = { generated_id = 987654321098765 }
}

variables {
  project_id      = "made-up-project-1234"
  deployer_member = "user:owner@example.com"
}

run "public_ui_on" {
  command = plan
  variables { expose_ui_publicly = true }

  assert {
    condition     = local.grain_config.iap_audience == "/projects/123456789012/global/backendServices/987654321098765"
    error_message = "iap_audience = ${local.grain_config.iap_audience}"
  }
  assert {
    condition     = local.grain_config.allowed_principals == "user:owner@example.com"
    error_message = "allowed_principals = ${local.grain_config.allowed_principals}"
  }
}

run "public_ui_off" {
  command = plan
  variables { expose_ui_publicly = false }

  assert {
    condition     = local.grain_config.iap_audience == ""
    error_message = "iap_audience = ${local.grain_config.iap_audience}"
  }
  assert {
    condition     = local.grain_config.allowed_principals == "user:owner@example.com"
    error_message = "allowed_principals = ${local.grain_config.allowed_principals}"
  }
}

run "ci_service_account_deployer" {
  command = plan
  variables {
    expose_ui_publicly = true
    deployer_member    = "serviceAccount:ci@made-up-project-1234.iam.gserviceaccount.com"
  }
  assert {
    condition     = local.grain_config.allowed_principals == ""
    error_message = "allowed_principals = ${local.grain_config.allowed_principals}"
  }
}

run "explicit_list" {
  command = plan
  variables {
    expose_ui_publicly = true
    allowed_principals = ["user:alice@example.com", "domain:corp.example"]
  }
  assert {
    condition     = local.grain_config.allowed_principals == "user:alice@example.com,domain:corp.example"
    error_message = "allowed_principals = ${local.grain_config.allowed_principals}"
  }
}
