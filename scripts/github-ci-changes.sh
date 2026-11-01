#!/usr/bin/env bash
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

# Evaluates per-group and per-job enablement booleans for .github/workflows/tests.yml
# and writes them to $GITHUB_OUTPUT (or stdout when GITHUB_OUTPUT is unset).
#
# Environment variables:
#   EVENT_NAME     - GitHub event name (pull_request, push, merge_group, workflow_dispatch)
#   BASE_SHA       - PR base commit SHA (pull_request)
#   HEAD_SHA       - PR head commit SHA (pull_request)
#   BASE_REF       - PR base branch name (pull_request fallback)
#   INPUT_JOBS     - Comma-separated job IDs or group names (workflow_dispatch)
#   CHANGED_FILES  - Optional newline-separated changed file list override (for unit tests)
#   GITHUB_OUTPUT  - Output file path populated by GitHub Actions

set -euo pipefail

event_name="${EVENT_NAME:-${GITHUB_EVENT_NAME:-pull_request}}"
base_sha="${BASE_SHA:-}"
head_sha="${HEAD_SHA:-}"
base_ref="${BASE_REF:-${GITHUB_BASE_REF:-}}"
input_jobs="${INPUT_JOBS:-}"

go_program=false
go_backend=false
go_unit=false
granule=false
frontend=false
lint=false
terraform=false

job_go=false
job_go_e2e=false
job_go_orchestrator=false
job_go_model=false
job_go_unit=false
job_postgres=false
job_run_host=false
job_host_script_container=false
job_ui_e2e=false
job_installer_e2e=false
job_installer=false
job_package_cache=false
job_real_vm=false
job_real_kubernetes=false

enable_all() {
  go_program=true
  go_backend=true
  go_unit=true
  granule=true
  frontend=true
  lint=true
  terraform=true

  job_go=true
  job_go_e2e=true
  job_go_orchestrator=true
  job_go_model=true
  job_go_unit=true
  job_postgres=true
  job_run_host=true
  job_host_script_container=true
  job_ui_e2e=true
  job_installer_e2e=true
  job_installer=true
  job_package_cache=true
  job_real_vm=true
  job_real_kubernetes=true
}

sync_jobs_from_groups() {
  job_go="$go_program"
  job_go_e2e="$go_program"
  job_ui_e2e="$go_program"
  job_installer_e2e="$go_program"
  job_installer="$go_program"

  job_go_orchestrator="$go_backend"
  job_go_model="$go_backend"
  job_postgres="$go_backend"
  job_run_host="$go_backend"
  job_host_script_container="$go_backend"
  job_package_cache="$go_backend"
  job_real_vm="$go_backend"
  job_real_kubernetes="$go_backend"

  job_go_unit="$go_unit"
}

# Mirrors *not-read-by-the-go-program in .grain/workflows/tests.yml:
#   - docs/**
#   - "!docs/*-surface.md"
#   - "*.md"
#   - LICENSE
#   - NOTICE
is_ignored_by_go_program() {
  local file="$1"
  if [[ "$file" == "LICENSE" || "$file" == "NOTICE" ]]; then
    return 0
  fi
  if [[ "$file" =~ ^[^/]+\.md$ ]]; then
    return 0
  fi
  if [[ "$file" == docs/* ]]; then
    if [[ "$file" =~ ^docs/[^/]*-surface\.md$ ]]; then
      return 1
    fi
    return 0
  fi
  return 1
}

# Mirrors *not-read-by-the-go-backend in .grain/workflows/tests.yml:
#   *not-read-by-the-go-program + ui/**
is_ignored_by_go_backend() {
  local file="$1"
  if is_ignored_by_go_program "$file"; then
    return 0
  fi
  if [[ "$file" == ui/* ]]; then
    return 0
  fi
  return 1
}

classify_file() {
  local file="${1#./}"
  if [[ -z "$file" ]]; then
    return 0
  fi

  if ! is_ignored_by_go_program "$file"; then
    go_program=true
  fi

  if ! is_ignored_by_go_backend "$file"; then
    go_backend=true
    go_unit=true
  fi

  if [[ "$file" == "ui/src/state.js" || "$file" == "ui/src/taskAttributes.js" ]]; then
    go_unit=true
  fi

  if [[ "$file" == third_party/granule/* || "$file" == "scripts/run-go-tests.sh" || "$file" == ".github/workflows/tests.yml" ]]; then
    granule=true
  fi

  if [[ "$file" == ui/* || "$file" == "Makefile" || "$file" == "scripts/warm-npm-cache.sh" || "$file" == ".github/workflows/tests.yml" ]]; then
    frontend=true
  fi

  if [[ ( "$file" == scripts/* && "$file" == *.sh ) || ( "$file" == deploy/terraform/* && "$file" == *.sh ) || "$file" == ".github/workflows/tests.yml" ]]; then
    lint=true
  fi

  if [[ "$file" == deploy/terraform/* || "$file" == ".github/workflows/tests.yml" ]]; then
    terraform=true
  fi
}

evaluate_workflow_dispatch() {
  local trimmed="${input_jobs//[[:space:]]/}"
  if [[ -z "$trimmed" ]]; then
    enable_all
    return 0
  fi

  local IFS=','
  local raw_tokens=()
  read -r -a raw_tokens <<< "$input_jobs"

  local selected_any=false
  for raw in "${raw_tokens[@]}"; do
    local token
    token="$(echo "$raw" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    if [[ -z "$token" ]]; then
      continue
    fi
    selected_any=true
    case "$token" in
      go)
        job_go=true
        go_program=true
        ;;
      go-e2e)
        job_go_e2e=true
        go_program=true
        ;;
      ui-e2e)
        job_ui_e2e=true
        go_program=true
        ;;
      installer|installer-e2e)
        job_installer_e2e=true
        job_installer=true
        go_program=true
        ;;
      go-orchestrator)
        job_go_orchestrator=true
        go_backend=true
        ;;
      go-model)
        job_go_model=true
        go_backend=true
        ;;
      postgres)
        job_postgres=true
        go_backend=true
        ;;
      run-host)
        job_run_host=true
        go_backend=true
        ;;
      host-script-container)
        job_host_script_container=true
        go_backend=true
        ;;
      package-cache)
        job_package_cache=true
        go_backend=true
        ;;
      real-vm)
        job_real_vm=true
        go_backend=true
        ;;
      real-kubernetes)
        job_real_kubernetes=true
        go_backend=true
        ;;
      go-unit|go_unit)
        job_go_unit=true
        go_unit=true
        ;;
      granule)
        granule=true
        ;;
      frontend)
        frontend=true
        ;;
      lint)
        lint=true
        ;;
      terraform)
        terraform=true
        ;;
      go_program)
        go_program=true
        job_go=true
        job_go_e2e=true
        job_ui_e2e=true
        job_installer_e2e=true
        job_installer=true
        ;;
      go_backend)
        go_backend=true
        job_go_orchestrator=true
        job_go_model=true
        job_postgres=true
        job_run_host=true
        job_host_script_container=true
        job_package_cache=true
        job_real_vm=true
        job_real_kubernetes=true
        ;;
      *)
        echo "github-ci-changes.sh: unknown job ID in inputs.jobs: '${token}'" >&2
        echo "Valid job IDs: go, go-e2e, go-orchestrator, go-model, go-unit, granule, postgres, run-host, host-script-container, frontend, ui-e2e, lint, terraform, installer-e2e (or installer), package-cache, real-vm, real-kubernetes" >&2
        exit 1
        ;;
    esac
  done

  if [[ "$selected_any" != "true" ]]; then
    enable_all
  fi
}

resolve_pr_diff() {
  if [[ -n "$base_sha" ]] && git cat-file -e "${base_sha}^{commit}" 2>/dev/null; then
    local target_head="HEAD"
    if [[ -n "$head_sha" ]] && git cat-file -e "${head_sha}^{commit}" 2>/dev/null; then
      target_head="$head_sha"
    fi
    if git diff --name-only "${base_sha}...${target_head}" 2>/dev/null; then
      return 0
    fi
    if git diff --name-only "${base_sha}" "${target_head}" 2>/dev/null; then
      return 0
    fi
  fi

  if [[ -n "$base_ref" ]] && git cat-file -e "origin/${base_ref}^{commit}" 2>/dev/null; then
    if git diff --name-only "origin/${base_ref}...HEAD" 2>/dev/null; then
      return 0
    fi
    if git diff --name-only "origin/${base_ref}" HEAD 2>/dev/null; then
      return 0
    fi
  fi

  if git rev-parse --verify HEAD^2 >/dev/null 2>&1; then
    if git diff --name-only HEAD^1...HEAD^2 2>/dev/null; then
      return 0
    fi
    if git diff --name-only HEAD^1 HEAD^2 2>/dev/null; then
      return 0
    fi
  fi

  return 1
}

case "$event_name" in
  workflow_dispatch)
    evaluate_workflow_dispatch
    ;;
  push|merge_group)
    enable_all
    ;;
  pull_request)
    if [[ -n "${CHANGED_FILES+x}" ]]; then
      while IFS= read -r line; do
        classify_file "$line"
      done <<< "$CHANGED_FILES"
      sync_jobs_from_groups
    else
      if diff_output="$(resolve_pr_diff)" && [[ -n "$diff_output" ]]; then
        while IFS= read -r line; do
          classify_file "$line"
        done <<< "$diff_output"
        sync_jobs_from_groups
      else
        echo "github-ci-changes.sh: could not resolve non-empty PR diff against base (base_sha=${base_sha:-unset}, base_ref=${base_ref:-unset}); failing open to all jobs" >&2
        enable_all
      fi
    fi
    ;;
  *)
    enable_all
    ;;
esac

emit_outputs() {
  cat <<EOF
go_program=${go_program}
go_backend=${go_backend}
go_unit=${go_unit}
granule=${granule}
frontend=${frontend}
lint=${lint}
terraform=${terraform}
go=${job_go}
go-e2e=${job_go_e2e}
go-orchestrator=${job_go_orchestrator}
go-model=${job_go_model}
go-unit=${job_go_unit}
postgres=${job_postgres}
run-host=${job_run_host}
host-script-container=${job_host_script_container}
ui-e2e=${job_ui_e2e}
installer-e2e=${job_installer_e2e}
installer=${job_installer}
package-cache=${job_package_cache}
real-vm=${job_real_vm}
real-kubernetes=${job_real_kubernetes}
EOF
}

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  emit_outputs >> "$GITHUB_OUTPUT"
fi
emit_outputs
