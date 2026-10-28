#!/usr/bin/env bash
#
# Single-command bootstrap and execution script for Grain remote model executors.
#
# Auto-detects platform OS (Linux / macOS) and architecture (amd64 / arm64),
# downloads the matching grain binary if needed, discovers installed agent CLIs
# (agy, claude, codex), and launches the remote executor (`grain executor`)
# or installs it as a persistent system service (systemd / launchd).
#
# Usage:
#   ./scripts/run-remote-executor.sh [options]
#   curl -fsSL https://<grain-host>/api/executors/install.sh | bash -s -- [options]
#
# Options:
#   -s, --server ADDR          Address or URL of the grain daemon executor listener (host:port or https://...)
#                              (or $GRAIN_SERVER / $GRAIN_ADDR)
#   -t, --token TOKEN          Shared executor token string (or $GRAIN_TOKEN). Discouraged: a
#                              token on the command line lands in shell history and is visible
#                              to other local users via ps; prefer --token-file or $GRAIN_TOKEN
#       --token-file PATH      Path to shared executor token file (or $GRAIN_TOKEN_FILE)
#       --team TEAM            Team associated with this remote executor (default: default, or $GRAIN_TEAM)
#   -n, --name NAME            Executor machine name; defaults to hostname (or $GRAIN_NAME)
#   -c, --concurrency NUM      Maximum number of concurrent agent tasks (default: 1, or $GRAIN_CONCURRENCY)
#   -i, --instance-id ID       Unique instance ID of this executor (or $GRAIN_INSTANCE_ID / $GRAIN_ID)
#       --grain-url URL        Grain UI / API base URL (e.g. http://grain.internal:8420 or https://... or $GRAIN_URL)
#       --audience AUDIENCE    Google ID token audience for Cloud Run / IAP proxy (or $GRAIN_AUDIENCE)
#       --binary NAME=PATH     Manual CLI binary mapping (-binary agy=/path/to/agy). Repeatable.
#       --grain-bin PATH       Explicit path to local grain binary (or $GRAIN_BIN)
#       --sha256 HEX           Expected SHA-256 of the downloaded grain binary or tarball
#                              (or $GRAIN_BINARY_SHA256). Without it, the checksum published
#                              next to the download (<url>.sha256) is required to match
#       --allow-unverified-binary
#                              Install a downloaded grain binary even when no checksum is
#                              available to verify it against (or $GRAIN_ALLOW_UNVERIFIED_BINARY=1)
#       --codex-auth-file PATH Path to codex auth.json (or $GRAIN_CODEX_AUTH_FILE)
#       --iap-audience AUD     GCP IAP OAuth client ID audience for ID token authentication (or $GRAIN_IAP_AUDIENCE)
#       --tls                  Connect to executor listener over TLS (or $GRAIN_TLS=1)
#       --ca-file PATH         PEM file of CA to verify grain certificate against (or $GRAIN_CA_FILE)
#       --tls-server-name NAME Server name to verify against TLS cert (or $GRAIN_TLS_SERVER_NAME)
#       --service              Install and start as a persistent system service (systemd / launchd)
#                              (or $GRAIN_SERVICE=1)
#       --install-agents       Attempt to auto-install missing agent CLIs (or $GRAIN_INSTALL_AGENTS=1)
#   -h, --help                 Show this help message
#
# Examples:
#   # Run interactively
#   ./scripts/run-remote-executor.sh --server grain.internal:8422 --token-file ~/.grain/token --team infrastructure
#
#   # Single-command bootstrap via curl
#   # (keep the token in a file so it never appears in shell history or ps output)
#   curl -fsSL https://grain.internal:8420/api/executors/install.sh | bash -s -- --server grain.internal:8422 --token-file ~/.grain/token --team default
#
#   # Install and enable as a systemd user service
#   ./scripts/run-remote-executor.sh --server grain.internal:8422 --token-file ~/.grain/token --service

set -euo pipefail

SERVER="${GRAIN_SERVER:-${GRAIN_ADDR:-}}"
TOKEN="${GRAIN_TOKEN:-}"
TOKEN_FILE="${GRAIN_TOKEN_FILE:-}"
TEAM="${GRAIN_TEAM:-default}"
NAME="${GRAIN_NAME:-}"
CONCURRENCY="${GRAIN_CONCURRENCY:-}"
INSTANCE_ID="${GRAIN_INSTANCE_ID:-${GRAIN_ID:-}}"
GRAIN_URL="${GRAIN_URL:-${GRAIN_GRAIN_URL:-}}"
AUDIENCE="${GRAIN_AUDIENCE:-}"
GRAIN_BIN="${GRAIN_BIN:-}"
CODEX_AUTH_FILE="${GRAIN_CODEX_AUTH_FILE:-}"
IAP_AUDIENCE="${GRAIN_IAP_AUDIENCE:-${IAP_AUDIENCE:-}}"
USE_TLS="${GRAIN_TLS:-0}"
CA_FILE="${GRAIN_CA_FILE:-}"
TLS_SERVER_NAME="${GRAIN_TLS_SERVER_NAME:-}"
INSTALL_SERVICE="${GRAIN_SERVICE:-0}"
INSTALL_AGENTS="${GRAIN_INSTALL_AGENTS:-0}"
BINARY_SHA256="${GRAIN_BINARY_SHA256:-}"
ALLOW_UNVERIFIED_BINARY="${GRAIN_ALLOW_UNVERIFIED_BINARY:-0}"
MANUAL_BINARIES=()
TOKEN_FROM_ARGV=0

usage() {
  cat <<'EOF'
Usage: run-remote-executor.sh [options]

Bootstrap and run a Grain remote model executor.

Options:
  -s, --server ADDR          Address or URL of grain executor listener (host:port or https://..., required)
  -t, --token TOKEN          Shared executor token string (discouraged: visible in shell
                             history and ps; prefer --token-file or GRAIN_TOKEN)
      --token-file PATH      Path to file holding shared executor token
      --team TEAM            Team associated with this executor (default: default)
  -n, --name NAME            Executor name (defaults to hostname)
  -c, --concurrency NUM      Maximum number of concurrent agent tasks (default: 1)
  -i, --instance-id ID       Unique instance ID of this executor (auto-generated if empty)
      --grain-url URL        Grain UI / API base URL (e.g. http://grain.internal:8420 or https://...)
      --audience AUDIENCE    Audience for Google ID tokens (Cloud Run / IAP proxy)
      --binary NAME=PATH     Manual CLI binary mapping (repeatable)
      --grain-bin PATH       Explicit path to local grain binary
      --sha256 HEX           Expected SHA-256 of the downloaded grain binary or tarball
      --allow-unverified-binary
                             Install a downloaded grain binary with no checksum to verify
      --codex-auth-file PATH Path to codex auth.json
      --iap-audience AUD     GCP IAP OAuth client ID audience for ID token authentication
      --tls                  Connect over TLS
      --ca-file PATH         PEM certificate authority file for TLS
      --tls-server-name NAME TLS certificate server name override
      --service              Install & start as persistent service (systemd/launchd)
      --install-agents       Auto-install missing agent CLIs if possible
  -h, --help                 Show this help message

Environment Variables:
  GRAIN_SERVER,
  GRAIN_TOKEN, GRAIN_TOKEN_FILE, GRAIN_TEAM, GRAIN_NAME,
  GRAIN_CONCURRENCY, GRAIN_INSTANCE_ID, GRAIN_ID,
  GRAIN_URL, GRAIN_AUDIENCE, GRAIN_IAP_AUDIENCE, GRAIN_BIN,
  GRAIN_CODEX_AUTH_FILE, GRAIN_TLS, GRAIN_CA_FILE, GRAIN_TLS_SERVER_NAME,
  GRAIN_SERVICE, GRAIN_INSTALL_AGENTS, GRAIN_BINARY_URL, GRAIN_BINARY_SHA256,
  GRAIN_ALLOW_UNVERIFIED_BINARY,
  GRAIN_INSTALL_DIR          the one directory to look for, and download, grain in
                             (default: PATH, then ~/.local/bin, /usr/local/bin and
                             ~/.grain/bin; downloads go to /usr/local/bin as root)
EOF
}

# Parse command line options
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    -s|--server)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires an address argument" >&2; exit 2; }
      SERVER="$2"
      shift 2
      ;;
    --server=*)
      SERVER="${1#*=}"
      shift
      ;;
    -t|--token)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a token argument" >&2; exit 2; }
      TOKEN="$2"
      TOKEN_FROM_ARGV=1
      shift 2
      ;;
    --token=*)
      TOKEN="${1#*=}"
      TOKEN_FROM_ARGV=1
      shift
      ;;
    --token-file)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a path argument" >&2; exit 2; }
      TOKEN_FILE="$2"
      shift 2
      ;;
    --token-file=*)
      TOKEN_FILE="${1#*=}"
      shift
      ;;
    --team)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a team argument" >&2; exit 2; }
      TEAM="$2"
      shift 2
      ;;
    --team=*)
      TEAM="${1#*=}"
      shift
      ;;
    -n|--name)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a name argument" >&2; exit 2; }
      NAME="$2"
      shift 2
      ;;
    --name=*)
      NAME="${1#*=}"
      shift
      ;;
    -c|--concurrency)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a number argument" >&2; exit 2; }
      CONCURRENCY="$2"
      shift 2
      ;;
    --concurrency=*)
      CONCURRENCY="${1#*=}"
      shift
      ;;
    -i|--instance-id|--id)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires an ID argument" >&2; exit 2; }
      INSTANCE_ID="$2"
      shift 2
      ;;
    --instance-id=*|--id=*)
      INSTANCE_ID="${1#*=}"
      shift
      ;;
    --grain-url)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a url argument" >&2; exit 2; }
      GRAIN_URL="$2"
      shift 2
      ;;
    --grain-url=*)
      GRAIN_URL="${1#*=}"
      shift
      ;;
    --audience)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires an audience argument" >&2; exit 2; }
      AUDIENCE="$2"
      shift 2
      ;;
    --audience=*)
      AUDIENCE="${1#*=}"
      shift
      ;;
    --grain-bin)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a path argument" >&2; exit 2; }
      GRAIN_BIN="$2"
      shift 2
      ;;
    --grain-bin=*)
      GRAIN_BIN="${1#*=}"
      shift
      ;;
    --sha256)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a checksum argument" >&2; exit 2; }
      BINARY_SHA256="$2"
      shift 2
      ;;
    --sha256=*)
      BINARY_SHA256="${1#*=}"
      shift
      ;;
    --allow-unverified-binary)
      ALLOW_UNVERIFIED_BINARY=1
      shift
      ;;
    --binary)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a NAME=PATH argument" >&2; exit 2; }
      MANUAL_BINARIES+=("$2")
      shift 2
      ;;
    --binary=*)
      MANUAL_BINARIES+=("${1#*=}")
      shift
      ;;
    --codex-auth-file)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a path argument" >&2; exit 2; }
      CODEX_AUTH_FILE="$2"
      shift 2
      ;;
    --codex-auth-file=*)
      CODEX_AUTH_FILE="${1#*=}"
      shift
      ;;
    --iap-audience)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires an audience argument" >&2; exit 2; }
      IAP_AUDIENCE="$2"
      shift 2
      ;;
    --iap-audience=*)
      IAP_AUDIENCE="${1#*=}"
      shift
      ;;
    --tls)
      USE_TLS=1
      shift
      ;;
    --ca-file)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a file argument" >&2; exit 2; }
      CA_FILE="$2"
      shift 2
      ;;
    --ca-file=*)
      CA_FILE="${1#*=}"
      shift
      ;;
    --tls-server-name)
      [ $# -ge 2 ] || { echo "run-remote-executor.sh: $1 requires a name argument" >&2; exit 2; }
      TLS_SERVER_NAME="$2"
      shift 2
      ;;
    --tls-server-name=*)
      TLS_SERVER_NAME="${1#*=}"
      shift
      ;;
    --service|--install-service)
      INSTALL_SERVICE=1
      shift
      ;;
    --install-agents)
      INSTALL_AGENTS=1
      shift
      ;;
    *)
      echo "run-remote-executor.sh: unrecognized argument: $1 (see --help)" >&2
      exit 2
      ;;
  esac
done

if [ "$TOKEN_FROM_ARGV" = "1" ]; then
  echo "Warning: --token puts the executor token on the command line, where it is saved in shell history" >&2
  echo "and visible to other local users via ps. Prefer --token-file PATH or the GRAIN_TOKEN variable." >&2
fi

if [ -n "$BINARY_SHA256" ]; then
  BINARY_SHA256="$(printf '%s' "$BINARY_SHA256" | tr '[:upper:]' '[:lower:]')"
  if ! [[ "$BINARY_SHA256" =~ ^[0-9a-f]{64}$ ]]; then
    echo "run-remote-executor.sh: --sha256 must be a 64-character hex SHA-256 digest" >&2
    exit 2
  fi
fi

detect_os() {
  local os
  os="$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    linux*) echo "linux" ;;
    darwin*) echo "darwin" ;;
    *)
      echo "unsupported"
      ;;
  esac
}

detect_arch() {
  local arch
  arch="$(uname -m 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      echo "unsupported"
      ;;
  esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

if [ "$OS" = "unsupported" ]; then
  echo "run-remote-executor.sh: unsupported operating system: $(uname -s)" >&2
  exit 1
fi

if [ "$ARCH" = "unsupported" ]; then
  echo "run-remote-executor.sh: unsupported system architecture: $(uname -m)" >&2
  exit 1
fi

echo "Detected platform: ${OS}/${ARCH}"

# Auto-detect IAP audience if not explicitly configured
if [ -z "$IAP_AUDIENCE" ]; then
  local_meta_host="${GCE_METADATA_HOST:-metadata.google.internal}"
  if command -v curl >/dev/null 2>&1; then
    meta_aud="$(curl -fs -m 2 -H "Metadata-Flavor: Google" "http://${local_meta_host}/computeMetadata/v1/instance/attributes/iap-audience" 2>/dev/null || true)"
    if [ -z "$meta_aud" ]; then
      meta_aud="$(curl -fs -m 2 -H "Metadata-Flavor: Google" "http://${local_meta_host}/computeMetadata/v1/project/attributes/iap-audience" 2>/dev/null || true)"
    fi
    if [ -n "$meta_aud" ]; then
      IAP_AUDIENCE="$meta_aud"
      echo "Auto-detected GCP IAP audience from instance metadata: $IAP_AUDIENCE"
    fi
  fi
fi

# fetch_url downloads URL to DEST with curl or wget, returning non-zero on
# any transport or HTTP failure.
fetch_url() {
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 2 --max-time 120 "$url" -o "$dest" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 120 -O "$dest" "$url" 2>/dev/null
  else
    return 1
  fi
}

# sha256_of prints the lowercase hex SHA-256 digest of FILE.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 -r "$1" | awk '{print $1}'
  else
    return 1
  fi
}

# verify_download checks FILE, downloaded from URL, against the pinned
# --sha256 digest when one was given, and otherwise against the checksum
# published beside it at URL.sha256. A download with no checksum to check
# is refused (status 1) unless --allow-unverified-binary was given; one
# whose checksum does not match is refused with status 2, which stops the
# install outright rather than moving on to the next source.
verify_download() {
  local url="$1" file="$2"
  local expected="$BINARY_SHA256" source="--sha256"
  if [ -z "$expected" ]; then
    local sum_file="$file.sha256"
    rm -f "$sum_file"
    if fetch_url "${url}.sha256" "$sum_file" && [ -s "$sum_file" ]; then
      expected="$(head -n 1 "$sum_file" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')"
      source="${url}.sha256"
    fi
    rm -f "$sum_file"
    if ! [[ "$expected" =~ ^[0-9a-f]{64}$ ]]; then
      if [ "$ALLOW_UNVERIFIED_BINARY" = "1" ] || [ "$ALLOW_UNVERIFIED_BINARY" = "true" ]; then
        echo "  Warning: no checksum available for $url; installing unverified (--allow-unverified-binary)" >&2
        return 0
      fi
      echo "  No checksum published at ${url}.sha256; refusing to install an unverified binary." >&2
      echo "  Pass --sha256 <digest> (or GRAIN_BINARY_SHA256), or --allow-unverified-binary to override." >&2
      return 1
    fi
  fi
  local actual
  if ! actual="$(sha256_of "$file")" || [ -z "$actual" ]; then
    echo "  No sha256sum, shasum or openssl found to verify the download; refusing to install it." >&2
    return 1
  fi
  if [ "$actual" != "$expected" ]; then
    echo "  Checksum mismatch for $url: expected $expected (from $source), got $actual." >&2
    return 2
  fi
  echo "  Verified SHA-256 $actual (from $source)" >&2
  return 0
}

# Determine grain binary
find_or_download_grain() {
  if [ -n "$GRAIN_BIN" ] && [ -x "$GRAIN_BIN" ]; then
    echo "$GRAIN_BIN"
    return 0
  fi

  # Check PATH or standard user/system directories -- or, given
  # GRAIN_INSTALL_DIR, that directory alone, which is where a download
  # goes as well.
  local install_dir="${GRAIN_INSTALL_DIR:-}"
  local candidates=("$install_dir/grain")
  if [ -z "$install_dir" ]; then
    candidates=(
      "$(command -v grain 2>/dev/null || true)"
      "$HOME/.local/bin/grain"
      "/usr/local/bin/grain"
      "$HOME/.grain/bin/grain"
    )
    install_dir="$HOME/.local/bin"
    if [ "$(id -u)" = "0" ]; then
      install_dir="/usr/local/bin"
    fi
  fi

  for cand in "${candidates[@]}"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done

  # Download grain binary
  mkdir -p "$install_dir"
  local target_bin="$install_dir/grain"
  local bin_name="grain-${OS}-${ARCH}"

  echo "Downloading grain binary (${bin_name})..." >&2

  local download_urls=()
  if [ -n "${GRAIN_BINARY_URL:-}" ]; then
    download_urls+=("$GRAIN_BINARY_URL")
  fi
  if [ -n "$GRAIN_URL" ]; then
    download_urls+=("${GRAIN_URL%/}/api/executors/bin/${bin_name}")
    download_urls+=("${GRAIN_URL%/}/install/${OS}/${ARCH}")
  fi
  download_urls+=(
    "https://github.com/ai-on-gke/grain/releases/latest/download/${bin_name}"
    "https://github.com/ai-on-gke/grain/releases/latest/download/${bin_name}.tar.gz"
  )

  # Stage downloads inside install_dir so the final rename onto target_bin
  # is atomic and never crosses a filesystem.
  local tmp_dir
  tmp_dir="$(mktemp -d "$install_dir/.grain-download.XXXXXX")"
  local tmp_file="$tmp_dir/download"
  local downloaded=0

  for url in "${download_urls[@]}"; do
    echo "  Trying $url..." >&2
    rm -f "$tmp_file"
    if ! fetch_url "$url" "$tmp_file" || [ ! -s "$tmp_file" ]; then
      continue
    fi
    local verify_status=0
    verify_download "$url" "$tmp_file" || verify_status=$?
    if [ "$verify_status" -eq 0 ]; then
      downloaded=1
      break
    fi
    if [ "$verify_status" -eq 2 ]; then
      rm -rf "$tmp_dir"
      echo "run-remote-executor.sh: refusing to install a grain binary that failed checksum verification" >&2
      return 1
    fi
  done

  if [ "$downloaded" -eq 1 ]; then
    # Check if downloaded file is tar.gz archive
    if file "$tmp_file" 2>/dev/null | grep -q "gzip compressed" || gzip -t "$tmp_file" 2>/dev/null; then
      mkdir "$tmp_dir/extract"
      tar -xzf "$tmp_file" -C "$tmp_dir/extract" grain 2>/dev/null || tar -xzf "$tmp_file" -C "$tmp_dir/extract"
      if [ ! -f "$tmp_dir/extract/grain" ]; then
        rm -rf "$tmp_dir"
        echo "run-remote-executor.sh: downloaded archive does not contain a grain binary" >&2
        return 1
      fi
      mv -f "$tmp_dir/extract/grain" "$tmp_dir/grain"
    else
      mv -f "$tmp_file" "$tmp_dir/grain"
    fi
    chmod 755 "$tmp_dir/grain"
    mv -f "$tmp_dir/grain" "$target_bin"
    rm -rf "$tmp_dir"
    echo "Installed grain binary to $target_bin" >&2
    echo "$target_bin"
    return 0
  fi

  rm -rf "$tmp_dir"
  echo "run-remote-executor.sh: unable to download grain binary. Please install grain or specify --grain-bin PATH" >&2
  return 1
}

GRAIN_BIN="$(find_or_download_grain)"
GRAIN_BIN_DIR="$(dirname "$GRAIN_BIN")"
export PATH="$GRAIN_BIN_DIR:$PATH"

# Auto-install missing agent CLIs if requested
if [ "$INSTALL_AGENTS" = "1" ] || [ "$INSTALL_AGENTS" = "true" ]; then
  if ! command -v claude >/dev/null 2>&1 && [ ! -x "$HOME/.local/bin/claude" ]; then
    echo "Attempting to install Claude CLI..."
    curl -fsSL https://claude.ai/install.sh | bash || true
  fi
  if ! command -v agy >/dev/null 2>&1 && [ ! -x "$HOME/.local/bin/agy" ]; then
    echo "Attempting to install Antigravity CLI..."
    curl -fsSL https://antigravity.google/cli/install.sh | bash || true
  fi
fi

# Discover agent binaries
find_binary() {
  local candidates=("$@")
  for cand in "${candidates[@]}"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
      echo "$cand"
      return 0
    fi
  done
  return 1
}

DISCOVERED_BINARIES=()
DISCOVERED_NAMES=()

# Always include grain binary itself for mcpserver tunneling
DISCOVERED_BINARIES+=("grain=${GRAIN_BIN}")
DISCOVERED_NAMES+=("grain")

# Antigravity CLI (agy)
AGY_CANDIDATES=(
  "${GRAIN_AGY_PATH:-${AGY_PATH:-}}"
  "$(command -v agy 2>/dev/null || true)"
  "$HOME/.local/bin/agy"
  "/usr/local/bin/agy"
  "/usr/bin/agy"
  "$HOME/.antigravity/bin/agy"
  "$HOME/.gemini/antigravity-cli/bin/agy"
)
if AGY_BIN="$(find_binary "${AGY_CANDIDATES[@]}")"; then
  DISCOVERED_BINARIES+=("agy=${AGY_BIN}")
  DISCOVERED_NAMES+=("agy")
fi

# Claude CLI (claude)
CLAUDE_CANDIDATES=(
  "${GRAIN_CLAUDE_PATH:-${CLAUDE_PATH:-}}"
  "$(command -v claude 2>/dev/null || true)"
  "$HOME/.local/bin/claude"
  "$HOME/.claude/bin/claude"
  "/usr/local/bin/claude"
  "/usr/bin/claude"
  "/opt/claude/bin/claude"
  "/opt/claude/claude"
)
if CLAUDE_BIN="$(find_binary "${CLAUDE_CANDIDATES[@]}")"; then
  DISCOVERED_BINARIES+=("claude=${CLAUDE_BIN}")
  DISCOVERED_NAMES+=("claude")
fi

# Codex CLI (codex)
CODEX_CANDIDATES=(
  "${GRAIN_CODEX_PATH:-${CODEX_PATH:-}}"
  "$(command -v codex 2>/dev/null || true)"
  "$HOME/.local/bin/codex"
  "/usr/local/bin/codex"
  "/usr/bin/codex"
  "$HOME/.codex/bin/codex"
  "$HOME/.npm-global/bin/codex"
)
if CODEX_BIN="$(find_binary "${CODEX_CANDIDATES[@]}")"; then
  DISCOVERED_BINARIES+=("codex=${CODEX_BIN}")
  DISCOVERED_NAMES+=("codex")
fi

# Merge manual binary overrides
for mb in "${MANUAL_BINARIES[@]}"; do
  mb_name="${mb%%=*}"
  # Replace existing discovered entry if same name, else add
  updated=0
  for i in "${!DISCOVERED_BINARIES[@]}"; do
    if [ "${DISCOVERED_BINARIES[i]%%=*}" = "$mb_name" ]; then
      DISCOVERED_BINARIES[i]="$mb"
      updated=1
      break
    fi
  done
  if [ "$updated" -eq 0 ]; then
    DISCOVERED_BINARIES+=("$mb")
    DISCOVERED_NAMES+=("$mb_name")
  fi
done

# Validate that at least one agent framework CLI was found
AGENT_FOUND=0
for n in "${DISCOVERED_NAMES[@]}"; do
  if [ "$n" = "agy" ] || [ "$n" = "claude" ] || [ "$n" = "codex" ]; then
    AGENT_FOUND=1
    break
  fi
done

if [ "$AGENT_FOUND" -eq 0 ]; then
  echo "Warning: No agent CLIs (agy, claude, codex) were detected on this machine." >&2
  echo "Dispatches requiring model execution may fail until an agent CLI is installed." >&2
  echo "To install an agent CLI:" >&2
  echo "  - Claude: curl -fsSL https://claude.ai/install.sh | bash" >&2
  echo "  - Antigravity: curl -fsSL https://antigravity.google/cli/install.sh | bash" >&2
  echo "  - Codex: npm install -g @openai/codex (or install codex binary)" >&2
fi

# Validate server address
if [ -z "$SERVER" ] && [ -n "$GRAIN_URL" ]; then
  SERVER="$GRAIN_URL"
fi

if [ -z "$SERVER" ]; then
  echo "run-remote-executor.sh: server address is required" >&2
  echo "Specify --server <host:port or https://...> or set GRAIN_SERVER" >&2
  exit 1
fi

if [ -z "$GRAIN_URL" ] && [[ "$SERVER" == http://* || "$SERVER" == https://* ]]; then
  GRAIN_URL="$SERVER"
fi

# Validate token or token file
if [ -z "$TOKEN_FILE" ]; then
  if [ -n "$TOKEN" ]; then
    mkdir -p "$HOME/.grain"
    chmod 700 "$HOME/.grain" 2>/dev/null || true
    token_suffix="${INSTANCE_ID:-${NAME:-}}"
    if [ -n "$token_suffix" ]; then
      TOKEN_FILE="$HOME/.grain/executor-${token_suffix}.token"
    else
      TOKEN_FILE="$HOME/.grain/executor.token"
    fi
    # Write through a mktemp file (created 0600) under a 077 umask and
    # rename it into place, so the token is never readable by anyone else,
    # not even for the moment between a write and a chmod.
    token_tmp="$(umask 077; mktemp "$HOME/.grain/.executor-token.XXXXXX")"
    ( umask 077; printf '%s' "$TOKEN" > "$token_tmp" )
    chmod 600 "$token_tmp"
    mv -f "$token_tmp" "$TOKEN_FILE"
  else
    echo "run-remote-executor.sh: token is required" >&2
    echo "Specify --token-file <path>, --token <token>, or set GRAIN_TOKEN / GRAIN_TOKEN_FILE" >&2
    exit 1
  fi
fi

if [ ! -f "$TOKEN_FILE" ]; then
  echo "run-remote-executor.sh: token file does not exist: $TOKEN_FILE" >&2
  exit 1
fi

# Construct executor CLI arguments
EXECUTOR_ARGS=(
  "-server" "$SERVER"
  "-token-file" "$TOKEN_FILE"
  "-team" "$TEAM"
)

if [ -n "$NAME" ]; then
  EXECUTOR_ARGS+=("-name" "$NAME")
fi

if [ -n "$CONCURRENCY" ]; then
  EXECUTOR_ARGS+=("-concurrency" "$CONCURRENCY")
fi

if [ -n "$INSTANCE_ID" ]; then
  EXECUTOR_ARGS+=("-instance-id" "$INSTANCE_ID")
fi

if [ -n "$GRAIN_URL" ]; then
  EXECUTOR_ARGS+=("-grain-url" "$GRAIN_URL")
fi

if [ -n "$AUDIENCE" ]; then
  EXECUTOR_ARGS+=("-audience" "$AUDIENCE")
fi

if [ -n "$CODEX_AUTH_FILE" ]; then
  EXECUTOR_ARGS+=("-codex-auth-file" "$CODEX_AUTH_FILE")
fi

if [ -n "$IAP_AUDIENCE" ]; then
  EXECUTOR_ARGS+=("-iap-audience" "$IAP_AUDIENCE")
fi

if [ "$USE_TLS" = "1" ] || [ "$USE_TLS" = "true" ]; then
  EXECUTOR_ARGS+=("-tls")
  if [ -n "$CA_FILE" ]; then
    EXECUTOR_ARGS+=("-ca-file" "$CA_FILE")
  fi
  if [ -n "$TLS_SERVER_NAME" ]; then
    EXECUTOR_ARGS+=("-tls-server-name" "$TLS_SERVER_NAME")
  fi
fi

for b in "${DISCOVERED_BINARIES[@]}"; do
  EXECUTOR_ARGS+=("-binary" "$b")
done

echo "Configured remote executor:"
echo "  Server:   $SERVER"
echo "  Team:     $TEAM"
echo "  Binaries: ${DISCOVERED_BINARIES[*]}"

# Handle service installation if requested
if [ "$INSTALL_SERVICE" = "1" ] || [ "$INSTALL_SERVICE" = "true" ]; then
  if [ "$OS" = "linux" ]; then
    if [ "$(id -u)" = "0" ]; then
      SERVICE_FILE="/etc/systemd/system/grain-executor.service"
      echo "Installing systemd system service at $SERVICE_FILE..."
      cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=Grain Remote Model Executor
After=network.target

[Service]
Type=simple
ExecStart=$GRAIN_BIN executor ${EXECUTOR_ARGS[*]}
Restart=always
RestartSec=5s
Environment=PATH=$PATH

[Install]
WantedBy=multi-user.target
EOF
      systemctl daemon-reload
      systemctl enable --now grain-executor
      echo "grain-executor system service installed and started."
    else
      USER_SYSTEMD_DIR="$HOME/.config/systemd/user"
      mkdir -p "$USER_SYSTEMD_DIR"
      SERVICE_FILE="$USER_SYSTEMD_DIR/grain-executor.service"
      echo "Installing systemd user service at $SERVICE_FILE..."
      cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=Grain Remote Model Executor
After=network.target

[Service]
Type=simple
ExecStart=$GRAIN_BIN executor ${EXECUTOR_ARGS[*]}
Restart=always
RestartSec=5s
Environment=PATH=$PATH

[Install]
WantedBy=default.target
EOF
      systemctl --user daemon-reload
      systemctl --user enable --now grain-executor
      echo "grain-executor user service installed and started."
      echo "Tip: Run 'loginctl enable-linger $(whoami)' to allow the service to run without an active login session."
    fi
    exit 0
  elif [ "$OS" = "darwin" ]; then
    PLIST_DIR="$HOME/Library/LaunchAgents"
    mkdir -p "$PLIST_DIR"
    PLIST_FILE="$PLIST_DIR/com.grain.executor.plist"
    echo "Installing macOS launchd agent at $PLIST_FILE..."

    # Build XML array of arguments
    PLIST_ARGS="        <string>$GRAIN_BIN</string>\n        <string>executor</string>\n"
    for arg in "${EXECUTOR_ARGS[@]}"; do
      PLIST_ARGS="${PLIST_ARGS}        <string>${arg}</string>\n"
    done

    cat <<EOF > "$PLIST_FILE"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.grain.executor</string>
    <key>ProgramArguments</key>
    <array>
$(printf '%s' "$PLIST_ARGS")
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$HOME/.grain/executor.log</string>
    <key>StandardErrorPath</key>
    <string>$HOME/.grain/executor.err</string>
</dict>
</plist>
EOF
    mkdir -p "$HOME/.grain"
    launchctl unload "$PLIST_FILE" 2>/dev/null || true
    launchctl load -w "$PLIST_FILE"
    echo "macOS launchd service installed and loaded."
    exit 0
  fi
fi

# Foreground execution
echo "Launching grain executor..."
exec "$GRAIN_BIN" executor "${EXECUTOR_ARGS[@]}"
