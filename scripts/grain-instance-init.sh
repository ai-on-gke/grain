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

# Boot-time initializer for a registry-provisioned Grain Compute Engine VM
# (grain-instance-init.service, design/design/hub.design.md).
#
# Runs as a oneshot systemd unit After=network-online.target and
# grain-block-metadata.service, and Before=grain-daemon.service:
#   1. Reads the `grain-config` JSON attribute from the GCE metadata server
#      (or exits 0 when absent so non-registry hosts are untouched).
#   2. Mounts /run/grain-secrets (tmpfs) and fetches `local-api-token`
#      (generating and storing the first version if Secret Manager returns
#      404) and `config-repo-key` into /run/grain-secrets (0600, owned by
#      $GRAIN_USER).
#   3. Clones the per-instance GitHub config repository (`config_repo`) into
#      ${GRAIN_DATA_DIR}/state-repo using the SSH deploy key at
#      /run/grain-secrets/config-repo-key and writes
#      ${GRAIN_DATA_DIR}/state-repo.json in mirror mode.
#   4. Configures and starts cloud-sql-proxy (--auto-iam-authn) over the
#      /cloudsql unix socket when `sql_instance` is set. A built-in password
#      user (`db_password_secret`, `instance_sql.cloud_sql_admin: false`)
#      instead has its password read from Secret Manager with the VM's
#      token and connects over `sql_private_ip` with `sslmode=require`; the
#      password itself is never in grain-config.
#   5. Writes /etc/grain/daemon.env (including GRAIN_DEPLOYMENT_OWNER and
#      -deployment-owner) for grain-daemon.service.

set -euo pipefail

GRAIN_METADATA_URL="${GRAIN_METADATA_URL:-http://metadata.google.internal/computeMetadata/v1}"
GRAIN_SM_API_URL="${GRAIN_SM_API_URL:-https://secretmanager.googleapis.com}"
GRAIN_DATA_DIR="${GRAIN_DATA_DIR:-/var/lib/grain/data}"
GRAIN_ETC_DIR="${GRAIN_ETC_DIR:-/etc/grain}"
GRAIN_SECRETS_RUN_DIR="${GRAIN_SECRETS_RUN_DIR:-/run/grain-secrets}"
GRAIN_CLOUDSQL_DIR="${GRAIN_CLOUDSQL_DIR:-/cloudsql}"
GRAIN_USER="${GRAIN_USER:-grain}"
GRAIN_GROUP="${GRAIN_GROUP:-}"
GRAIN_CONFIG_FILE="${GRAIN_CONFIG_FILE:-}"
GRAIN_ACCESS_TOKEN="${GRAIN_ACCESS_TOKEN:-}"
GRAIN_SQL_PROXY_TIMEOUT="${GRAIN_SQL_PROXY_TIMEOUT:-60}"
GRAIN_SKIP_TMPFS_MOUNT="${GRAIN_SKIP_TMPFS_MOUNT:-0}"
GRAIN_SKIP_SYSTEMCTL="${GRAIN_SKIP_SYSTEMCTL:-0}"
GRAIN_SM_ATTEMPTS="${GRAIN_SM_ATTEMPTS:-6}"
GRAIN_SM_RETRY_DELAY="${GRAIN_SM_RETRY_DELAY:-1}"

log() {
  echo "grain-instance-init: $*" >&2
}

die() {
  echo "grain-instance-init: ERROR: $*" >&2
  exit 1
}

resolve_grain_account() {
  if ! id -u "$GRAIN_USER" >/dev/null 2>&1; then
    GRAIN_USER="$(id -un)"
  fi
  if [ -z "$GRAIN_GROUP" ]; then
    GRAIN_GROUP="$(id -gn "$GRAIN_USER")"
  fi
}

run_as_grain() {
  if [ "$(id -un)" = "$GRAIN_USER" ]; then
    "$@"
  elif command -v runuser >/dev/null 2>&1; then
    runuser -u "$GRAIN_USER" -- "$@"
  else
    sudo -u "$GRAIN_USER" -H "$@"
  fi
}

write_grain_file() {
  local mode="$1"
  local path="$2"
  local content="$3"
  local dir
  dir="$(dirname "$path")"
  install -d -m 0750 -o "$GRAIN_USER" -g "$GRAIN_GROUP" "$dir"
  local tmp="${path}.tmp.$$"
  umask 0077
  printf '%s' "$content" > "$tmp"
  chown "$GRAIN_USER:$GRAIN_GROUP" "$tmp"
  chmod "$mode" "$tmp"
  mv -f "$tmp" "$path"
}

fetch_metadata() {
  local rel_path="$1"
  local status_file
  status_file="$(mktemp)"
  local body_file
  body_file="$(mktemp)"
  local http_code
  http_code="$(
    curl -sS -o "$body_file" -w '%{http_code}' \
      -H "Metadata-Flavor: Google" \
      "${GRAIN_METADATA_URL%/}/${rel_path#/}" 2>/dev/null || echo "000"
  )"
  rm -f "$status_file"
  if [ "$http_code" = "200" ]; then
    cat "$body_file"
    rm -f "$body_file"
    return 0
  fi
  rm -f "$body_file"
  if [ "$http_code" = "404" ]; then
    return 44
  fi
  return 1
}

read_grain_config() {
  if [ -n "$GRAIN_CONFIG_FILE" ]; then
    if [ ! -f "$GRAIN_CONFIG_FILE" ]; then
      log "GRAIN_CONFIG_FILE=$GRAIN_CONFIG_FILE does not exist; skipping instance init"
      return 1
    fi
    cat "$GRAIN_CONFIG_FILE"
    return 0
  fi

  local raw=""
  local rc=0
  raw="$(fetch_metadata "instance/attributes/grain-config")" || rc=$?
  if [ "$rc" -eq 44 ] || [ -z "${raw//[[:space:]]/}" ]; then
    log "no grain-config instance metadata attribute present; skipping instance init"
    return 1
  fi
  if [ "$rc" -ne 0 ]; then
    die "failed to read grain-config from metadata server (${GRAIN_METADATA_URL})"
  fi
  printf '%s' "$raw"
  return 0
}

fetch_access_token() {
  if [ -n "$GRAIN_ACCESS_TOKEN" ]; then
    printf '%s' "$GRAIN_ACCESS_TOKEN"
    return 0
  fi
  local token_json
  token_json="$(fetch_metadata "instance/service-accounts/default/token")" \
    || die "failed to fetch service account access token from metadata server"
  local token
  token="$(printf '%s' "$token_json" | jq -r '.access_token // empty')"
  [ -n "$token" ] || die "metadata server returned an empty access_token"
  printf '%s' "$token"
}

fetch_service_account_email() {
  local email=""
  email="$(fetch_metadata "instance/service-accounts/default/email" || true)"
  printf '%s' "$email" | tr -d '[:space:]'
}

prepare_secrets_run_dir() {
  local gid
  gid="$(id -g "$GRAIN_USER")"
  mkdir -p "$GRAIN_SECRETS_RUN_DIR"
  if [ "$GRAIN_SKIP_TMPFS_MOUNT" != "1" ] && [ "$GRAIN_SECRETS_RUN_DIR" = "/run/grain-secrets" ] && [ "$(id -u)" -eq 0 ]; then
    if ! mountpoint -q "$GRAIN_SECRETS_RUN_DIR" 2>/dev/null; then
      mount -t tmpfs -o "size=16m,mode=0750,uid=0,gid=${gid},nosuid,nodev,noexec" tmpfs "$GRAIN_SECRETS_RUN_DIR"
    fi
  fi
  chown "$(id -u):$GRAIN_GROUP" "$GRAIN_SECRETS_RUN_DIR" 2>/dev/null || true
  chmod 0750 "$GRAIN_SECRETS_RUN_DIR"
}

decode_base64() {
  if base64 --help 2>&1 | grep -q -- '--decode'; then
    base64 --decode
  else
    base64 -D
  fi
}

encode_base64() {
  base64 | tr -d '\r\n'
}

# sm_access_latest fetches the latest version of a Secret Manager secret
# resource (projects/<project>/secrets/<name>). Returns 0 and prints the
# decoded secret payload on HTTP 200; returns 44 on HTTP 404; exits on any
# other error. A transient failure (no connection, HTTP 429 or 5xx) is
# retried GRAIN_SM_ATTEMPTS times with doubling delays: this unit runs once
# per boot and grain-daemon.service starts after it whether or not it
# succeeded, so a single Secret Manager blip must not leave the VM without
# its daemon.env.
sm_access_latest() {
  local secret_name="$1"
  local token="$2"
  local body_file
  body_file="$(mktemp)"
  local url="${GRAIN_SM_API_URL%/}/v1/${secret_name#/}/versions/latest:access"
  local http_code attempt=1 delay="$GRAIN_SM_RETRY_DELAY"
  while :; do
    http_code="$(
      curl -sS -o "$body_file" -w '%{http_code}' \
        -H "Authorization: Bearer ${token}" \
        -H "Accept: application/json" \
        "$url" 2>/dev/null || echo "000"
    )"
    case "$http_code" in
      000 | 429 | 5??) ;;
      *) break ;;
    esac
    [ "$attempt" -lt "$GRAIN_SM_ATTEMPTS" ] || break
    log "Secret Manager access ${secret_name} returned HTTP ${http_code}; retrying in ${delay}s (attempt ${attempt}/${GRAIN_SM_ATTEMPTS})"
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
  if [ "$http_code" = "200" ]; then
    local b64
    b64="$(jq -r '.payload.data // empty' "$body_file")"
    rm -f "$body_file"
    printf '%s' "$b64" | decode_base64
    return 0
  fi
  if [ "$http_code" = "404" ]; then
    rm -f "$body_file"
    return 44
  fi
  local err_body
  err_body="$(cat "$body_file" 2>/dev/null || true)"
  rm -f "$body_file"
  die "Secret Manager access ${secret_name} failed (HTTP ${http_code}): ${err_body}"
}

sm_add_version() {
  local secret_name="$1"
  local token="$2"
  local raw_value="$3"
  local b64
  b64="$(printf '%s' "$raw_value" | encode_base64)"
  local body_file
  body_file="$(mktemp)"
  local url="${GRAIN_SM_API_URL%/}/v1/${secret_name#/}:addVersion"
  local http_code
  http_code="$(
    curl -sS -o "$body_file" -w '%{http_code}' \
      -X POST \
      -H "Authorization: Bearer ${token}" \
      -H "Content-Type: application/json" \
      -d "{\"payload\":{\"data\":\"${b64}\"}}" \
      "$url" 2>/dev/null || echo "000"
  )"
  if [ "$http_code" != "200" ] && [ "$http_code" != "201" ]; then
    local err_body
    err_body="$(cat "$body_file" 2>/dev/null || true)"
    rm -f "$body_file"
    die "Secret Manager addVersion ${secret_name} failed (HTTP ${http_code}): ${err_body}"
  fi
  rm -f "$body_file"
}

ensure_local_api_token() {
  local sm_prefix="$1"
  local access_token="$2"
  local secret_name="${sm_prefix}local-api-token"
  local token_value=""
  local rc=0
  token_value="$(sm_access_latest "$secret_name" "$access_token")" || rc=$?
  if [ "$rc" -eq 44 ] || [ -z "${token_value//[[:space:]]/}" ]; then
    log "secret ${secret_name} has no version yet; generating initial local-api-token"
    token_value="$(openssl rand -hex 32)"
    sm_add_version "$secret_name" "$access_token" "$token_value"
  elif [ "$rc" -ne 0 ]; then
    die "unable to read ${secret_name}"
  fi
  token_value="$(printf '%s' "$token_value" | tr -d '\r\n')"
  write_grain_file 0600 "${GRAIN_SECRETS_RUN_DIR}/local-api-token" "${token_value}"$'\n'
  write_grain_file 0600 "${GRAIN_DATA_DIR}/local-api-token" "${token_value}"$'\n'
}

ensure_config_repo_key() {
  local sm_prefix="$1"
  local access_token="$2"
  local required="$3"
  local secret_name="${sm_prefix}config-repo-key"
  local key_value=""
  local rc=0
  key_value="$(sm_access_latest "$secret_name" "$access_token")" || rc=$?
  if [ "$rc" -eq 44 ]; then
    if [ "$required" = "1" ]; then
      die "config_repo is set in grain-config, but Secret Manager secret ${secret_name} has no version"
    fi
    return 1
  elif [ "$rc" -ne 0 ]; then
    die "unable to read ${secret_name}"
  fi
  if [ -z "${key_value//[[:space:]]/}" ]; then
    if [ "$required" = "1" ]; then
      die "Secret Manager secret ${secret_name} is empty"
    fi
    return 1
  fi
  # Ensure trailing newline required by OpenSSH for PEM/OpenSSH private keys.
  case "$key_value" in
    *$'\n') ;;
    *) key_value="${key_value}"$'\n' ;;
  esac
  write_grain_file 0600 "${GRAIN_SECRETS_RUN_DIR}/config-repo-key" "$key_value"
  return 0
}

clone_config_repo() {
  local config_repo="$1"
  local branch="$2"
  local ssh_key="${GRAIN_SECRETS_RUN_DIR}/config-repo-key"
  local repo_dir="${GRAIN_DATA_DIR}/state-repo"
  local git_ssh="ssh -i ${ssh_key} -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new"

  install -d -m 0750 -o "$GRAIN_USER" -g "$GRAIN_GROUP" "$GRAIN_DATA_DIR"
  rm -rf "$repo_dir"

  log "cloning config repository ${config_repo} (branch ${branch}) into ${repo_dir}"
  if ! run_as_grain env GIT_SSH_COMMAND="$git_ssh" git clone --branch "$branch" "$config_repo" "$repo_dir" >/dev/null 2>&1; then
    # Newly created empty repository on GitHub has no branch yet; clone and
    # initialize the target branch pointing at origin.
    rm -rf "$repo_dir"
    run_as_grain env GIT_SSH_COMMAND="$git_ssh" git clone "$config_repo" "$repo_dir" >/dev/null
    run_as_grain git -C "$repo_dir" checkout -B "$branch" >/dev/null 2>&1 || true
  fi

  local settings_json
  settings_json="$(
    jq -n \
      --arg remote "$config_repo" \
      --arg branch "$branch" \
      --arg mode "mirror" \
      --arg sshKeyFile "$ssh_key" \
      '{remote: $remote, branch: $branch, mode: $mode, sshKeyFile: $sshKeyFile}'
  )"
  write_grain_file 0600 "${GRAIN_DATA_DIR}/state-repo.json" "${settings_json}"$'\n'
}

# fetch_db_password prints the latest version of the built-in database
# user's password secret (`db_password_secret`). The registry keeps the
# password out of grain-config because GCE metadata is readable by anyone
# with compute.instances.get on the project.
fetch_db_password() {
  local secret_name="$1"
  local access_token="$2"
  local value=""
  local rc=0
  value="$(sm_access_latest "$secret_name" "$access_token")" || rc=$?
  if [ "$rc" -eq 44 ]; then
    die "grain-config sets db_password_secret, but Secret Manager secret ${secret_name} has no version"
  elif [ "$rc" -ne 0 ]; then
    die "unable to read ${secret_name}"
  fi
  value="$(printf '%s' "$value" | tr -d '\r\n')"
  [ -n "$value" ] || die "Secret Manager secret ${secret_name} is empty"
  printf '%s' "$value"
}

# password_db_dsn builds the private-IP DSN for a built-in password user.
# The connection always requires TLS (Cloud SQL accepts it on its private
# IP by default), so neither the password nor any query crosses the VPC in
# clear.
password_db_dsn() {
  local user="$1" password="$2" host="$3" name="$4"
  local enc_user enc_pw
  enc_user="$(printf '%s' "$user" | jq -sRr @uri)"
  enc_pw="$(printf '%s' "$password" | jq -sRr @uri)"
  printf 'postgres://%s:%s@%s:5432/%s?sslmode=require' "$enc_user" "$enc_pw" "$host" "$name"
}

configure_cloud_sql_proxy() {
  local sql_instance="$1"
  [ -n "$sql_instance" ] || return 0

  install -d -m 0755 "$GRAIN_ETC_DIR"
  install -d -m 0755 "$GRAIN_CLOUDSQL_DIR"
  if id -u cloudsqlproxy >/dev/null 2>&1; then
    chown cloudsqlproxy:cloudsqlproxy "$GRAIN_CLOUDSQL_DIR" 2>/dev/null || true
  fi

  cat > "${GRAIN_ETC_DIR}/cloud-sql-proxy.env" <<EOF
CLOUD_SQL_INSTANCE=${sql_instance}
EOF
  chmod 0644 "${GRAIN_ETC_DIR}/cloud-sql-proxy.env"

  if [ "$GRAIN_SKIP_SYSTEMCTL" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    log "starting cloud-sql-proxy.service for ${sql_instance}"
    systemctl restart cloud-sql-proxy.service
    local socket_path="${GRAIN_CLOUDSQL_DIR}/${sql_instance}/.s.PGSQL.5432"
    local elapsed=0
    while [ ! -S "$socket_path" ] && [ "$elapsed" -lt "$GRAIN_SQL_PROXY_TIMEOUT" ]; do
      sleep 1
      elapsed=$((elapsed + 1))
    done
    if [ ! -S "$socket_path" ]; then
      die "cloud-sql-proxy socket ${socket_path} did not appear within ${GRAIN_SQL_PROXY_TIMEOUT}s"
    fi
  fi
}

main() {
  local config_json=""
  if ! config_json="$(read_grain_config)"; then
    return 0
  fi

  resolve_grain_account

  local instance_name deployment_owner owner_group issuer instance_domain audience jwks_url management_url
  local sql_instance db_name db_user db_dsn db_password_secret sql_private_ip sm_prefix object_store config_repo config_repo_branch

  instance_name="$(printf '%s' "$config_json" | jq -r '.instance // .name // empty')"
  deployment_owner="$(printf '%s' "$config_json" | jq -r '.deployment_owner // .owner_email // empty')"
  owner_group="$(printf '%s' "$config_json" | jq -r '.owner_group // empty')"
  issuer="$(printf '%s' "$config_json" | jq -r '.issuer // .registry_issuer // empty')"
  instance_domain="$(printf '%s' "$config_json" | jq -r '.instance_domain // empty')"
  audience="$(printf '%s' "$config_json" | jq -r '.audience // .registry_audience // empty')"
  jwks_url="$(printf '%s' "$config_json" | jq -r '.jwks_url // .registry_keys_url // empty')"
  management_url="$(printf '%s' "$config_json" | jq -r '.management_url // empty')"
  sql_instance="$(printf '%s' "$config_json" | jq -r '.sql_instance // .cloud_sql_instance // empty')"
  db_name="$(printf '%s' "$config_json" | jq -r '.db_name // empty')"
  db_user="$(printf '%s' "$config_json" | jq -r '.db_user // empty')"
  db_dsn="$(printf '%s' "$config_json" | jq -r '.db_dsn // empty')"
  db_password_secret="$(printf '%s' "$config_json" | jq -r '.db_password_secret // empty')"
  sql_private_ip="$(printf '%s' "$config_json" | jq -r '.sql_private_ip // .GRAIN_SQL_PRIVATE_IP // empty')"
  sm_prefix="$(printf '%s' "$config_json" | jq -r '.sm_prefix // .secrets_sm_prefix // empty')"
  object_store="$(printf '%s' "$config_json" | jq -r '.object_store // empty')"
  config_repo="$(printf '%s' "$config_json" | jq -r '.config_repo // empty')"
  config_repo_branch="$(printf '%s' "$config_json" | jq -r '.config_repo_branch // "main"')"

  if [ -z "$deployment_owner" ]; then
    die "grain-config metadata is missing required field deployment_owner"
  fi
  if [[ "$deployment_owner" != *"@"* ]]; then
    die "grain-config deployment_owner=${deployment_owner} must be an email address"
  fi

  if [ -z "$audience" ] && [ -n "$instance_name" ] && [ -n "$instance_domain" ]; then
    audience="https://${instance_name}.${instance_domain#.}"
  fi
  if [ -z "$jwks_url" ] && [ -n "$issuer" ]; then
    jwks_url="${issuer%/}/.well-known/jwks.json"
  fi
  if [ -z "$management_url" ] && [ -n "$issuer" ] && [ -n "$instance_name" ]; then
    management_url="${issuer%/}/entries/${instance_name}"
  fi
  if [ -z "$db_name" ] && [ -n "$instance_name" ]; then
    db_name="grain_${instance_name//-/_}"
  fi
  if [ -n "$db_password_secret" ]; then
    # Built-in password user: the password stays in Secret Manager and the
    # DSN is built below, after it has been fetched.
    [ -n "$db_user" ] || die "grain-config sets db_password_secret but not db_user"
    [ -n "$db_name" ] || die "grain-config sets db_password_secret but not db_name"
    [ -n "$sql_private_ip" ] || die "grain-config sets db_password_secret but not sql_private_ip"
  elif [ -z "$db_dsn" ] && [ -n "$sql_instance" ]; then
    if [ -z "$db_user" ]; then
      local sa_email
      sa_email="$(fetch_service_account_email)"
      db_user="${sa_email%.gserviceaccount.com}"
    fi
    [ -n "$db_user" ] || die "unable to determine Cloud SQL IAM db_user for sql_instance=${sql_instance}"
    [ -n "$db_name" ] || die "unable to determine db_name for sql_instance=${sql_instance}"
    db_dsn="postgres://${db_user}@/${db_name}?host=${GRAIN_CLOUDSQL_DIR}/${sql_instance}"
  fi

  prepare_secrets_run_dir
  install -d -m 0750 -o "$GRAIN_USER" -g "$GRAIN_GROUP" "$GRAIN_DATA_DIR"

  local access_token=""
  if [ -n "$db_password_secret" ]; then
    access_token="$(fetch_access_token)"
    local db_password
    db_password="$(fetch_db_password "$db_password_secret" "$access_token")"
    db_dsn="$(password_db_dsn "$db_user" "$db_password" "$sql_private_ip" "$db_name")"
    unset db_password
  fi

  if [ -n "$sm_prefix" ] || [ -n "$config_repo" ]; then
    [ -n "$sm_prefix" ] || die "grain-config must specify sm_prefix to fetch Secret Manager credentials"
    [ -n "$access_token" ] || access_token="$(fetch_access_token)"
    ensure_local_api_token "$sm_prefix" "$access_token"
    local need_repo_key=0
    [ -n "$config_repo" ] && need_repo_key=1
    if ensure_config_repo_key "$sm_prefix" "$access_token" "$need_repo_key"; then
      if [ -n "$config_repo" ]; then
        clone_config_repo "$config_repo" "$config_repo_branch"
      fi
    fi
  fi

  configure_cloud_sql_proxy "$sql_instance"

  local allowed_principals="user:${deployment_owner}"
  if [ -n "$owner_group" ]; then
    allowed_principals="${allowed_principals},group:${owner_group}"
  fi

  install -d -m 0755 "$GRAIN_ETC_DIR"
  local env_file="${GRAIN_ETC_DIR}/daemon.env"
  local tmp_env="${env_file}.tmp.$$"
  umask 0077
  {
    echo "# Generated by grain-instance-init.sh from GCE grain-config metadata."
    echo "GRAIN_DEPLOYMENT_OWNER=${deployment_owner}"
    echo "GRAIN_ALLOWED_PRINCIPALS=${allowed_principals}"
    # Under the Grain Hub's names and, for one release, the Grain
    # Registry's too: the instance's grain may predate the rename.
    [ -n "$issuer" ] && echo "GRAIN_HUB_ISSUER=${issuer}"
    [ -n "$audience" ] && echo "GRAIN_HUB_AUDIENCE=${audience}"
    [ -n "$jwks_url" ] && echo "GRAIN_HUB_KEYS_URL=${jwks_url}"
    [ -n "$issuer" ] && echo "GRAIN_REGISTRY_ISSUER=${issuer}"
    [ -n "$audience" ] && echo "GRAIN_REGISTRY_AUDIENCE=${audience}"
    [ -n "$jwks_url" ] && echo "GRAIN_REGISTRY_KEYS_URL=${jwks_url}"
    if [ -n "$issuer" ] && [ -n "$audience" ] && [ -n "$jwks_url" ]; then
      echo "GRAIN_REQUIRE_VERIFIER=registry"
    fi
    echo "GRAIN_DISABLE_SELF_UPGRADE=true"
    [ -n "$management_url" ] && echo "GRAIN_MANAGEMENT_URL=${management_url}"
    if [ -n "$db_dsn" ]; then
      echo "GRAIN_DB_DRIVER=postgres"
      echo "GRAIN_DB_DSN=${db_dsn}"
    fi
    if [ -n "$sm_prefix" ]; then
      echo "GRAIN_SECRETS_BACKEND=secretmanager"
      echo "GRAIN_SECRETS_SM_PREFIX=${sm_prefix}"
      echo "GRAIN_LOCAL_API_TOKEN_FILE=${GRAIN_SECRETS_RUN_DIR}/local-api-token"
    fi
    if [ -f "${GRAIN_SECRETS_RUN_DIR}/config-repo-key" ]; then
      echo "GRAIN_STATE_REPO_SSH_KEY=${GRAIN_SECRETS_RUN_DIR}/config-repo-key"
    fi
    [ -n "$object_store" ] && echo "GRAIN_OBJECT_STORE=${object_store}"
    echo "GRAIN_EXTRA_DAEMON_ARGS=-deployment-owner=${deployment_owner}"
    echo "GRAIN_EXTRA_DOCKER_RUN_ARGS=--env-file ${env_file} --volume ${GRAIN_SECRETS_RUN_DIR}:${GRAIN_SECRETS_RUN_DIR}:ro --volume ${GRAIN_CLOUDSQL_DIR}:${GRAIN_CLOUDSQL_DIR}"
  } > "$tmp_env"
  chmod 0600 "$tmp_env"
  mv -f "$tmp_env" "$env_file"
  log "wrote ${env_file} for instance ${instance_name:-unknown} (owner ${deployment_owner})"
}

main "$@"
