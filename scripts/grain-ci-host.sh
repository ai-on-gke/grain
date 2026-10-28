# shellcheck shell=bash
# Sourced, not run: what a `docker build` inside a Grain CI sandbox needs
# from the host it runs on. scripts/release-images.sh and
# scripts/ci-docker-build.sh both source it, so that a build outside the
# release (the nightly upgrade test's) gets the same host the release's
# own build does rather than a copy of it that drifts.
#
# Two things go wrong without it. The package-cache proxy re-signs
# registry TLS with Grain's CA, so the Dockerfile needs that CA as
# .grain-ca.crt in its build context or `npm ci` fails with
# SELF_SIGNED_CERT_IN_CHAIN: stage_grain_ca puts it there, and
# cleanup_ca, which the caller traps on EXIT, takes it away again. And
# a sandbox's default interface says 1500 where the path to some
# upstreams (dl.k8s.io, github.com) carries less, so a TLS handshake to
# them stalls until curl times out: ensure_network_mtu clamps it.
#
# The caller sets repo_root (the build context the CA is staged into)
# and, if it has one, dry_run.

ca_copied=false

cleanup_ca() {
  if [ "${ca_copied:-false}" = true ] && command -v rm >/dev/null 2>&1; then
    rm -f "${repo_root:-.}/.grain-ca.crt" 2>/dev/null || true
    ca_copied=false
  fi
}

# Grain's CA as .grain-ca.crt in $repo_root, where the Dockerfiles'
# `COPY Dockerfile .grain-ca.cr[t] /tmp/` picks it up. One already there
# is somebody else's and is left alone, by this and by cleanup_ca.
stage_grain_ca() {
  if [ -f /usr/local/share/ca-certificates/grain-ca.crt ] && [ ! -f "$repo_root/.grain-ca.crt" ] && command -v cp >/dev/null 2>&1; then
    cp /usr/local/share/ca-certificates/grain-ca.crt "$repo_root/.grain-ca.crt" 2>/dev/null || true
    ca_copied=true
  fi
}

# The ensure_* functions here (and release-images.sh's
# ensure_direct_registry_route) rewrite /etc/resolv.conf, install a
# CA fetched from 172.17.0.1 into the system trust store, clamp the default
# interface's MTU (and dockerd's, in daemon.json), add an iptables mangle
# rule and append a registry address to /etc/hosts. That is what a Grain
# CI sandbox behind the package cache needs, and what an operator's
# workstation does not: the 2026-10-05 walkthrough found ghcr.io in
# /etc/hosts after a run aimed at localhost:5000. So they run only in
# Grain CI, which sets GRAIN_CI=true on every job (pkg/ci runEnv), or when
# a hand run asks for them with RELEASE_IMAGES_TUNE_HOST=1 -- and never
# for a dry run, which promises to change nothing.
tune_host() {
  [ "${dry_run:-false}" != true ] || return 1
  [ "${GRAIN_CI:-}" = true ] && return 0
  case "${RELEASE_IMAGES_TUNE_HOST:-}" in
    1 | true | yes) return 0 ;;
  esac
  return 1
}

ensure_network_mtu() {
  tune_host || return 0
  local iface
  iface="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}' | head -n1 || true)"
  iface="${iface:-eth0}"
  local mtu_clamped=false
  if [ -r "/sys/class/net/$iface/mtu" ]; then
    local current_mtu
    current_mtu="$(cat "/sys/class/net/$iface/mtu" 2>/dev/null || true)"
    if [ -n "$current_mtu" ] && [ "$current_mtu" -gt 1460 ] 2>/dev/null; then
      if [ "$(id -u)" = 0 ]; then
        ip link set dev "$iface" mtu 1460 2>/dev/null || true
        mtu_clamped=true
      elif command -v sudo >/dev/null 2>&1; then
        sudo -n ip link set dev "$iface" mtu 1460 2>/dev/null || true
        mtu_clamped=true
      fi
    fi
  fi

  # dockerd is restarted only when daemon.json actually changed: a restart
  # stops every container on the host, and one that already says 1460 (as
  # a Grain CI sandbox's does) gains nothing from it.
  if [ "$mtu_clamped" = true ] && [ -f /etc/docker/daemon.json ]; then
    local daemon_before
    daemon_before="$(cksum < /etc/docker/daemon.json 2>/dev/null || true)"
    if [ "$(id -u)" = 0 ]; then
      if command -v jq >/dev/null 2>&1; then
        jq '.mtu = 1460' /etc/docker/daemon.json > /etc/docker/daemon.json.tmp 2>/dev/null && mv /etc/docker/daemon.json.tmp /etc/docker/daemon.json
      else
        sed -i 's/"mtu": *[0-9]*/"mtu": 1460/' /etc/docker/daemon.json 2>/dev/null || true
      fi
      if [ "$(cksum < /etc/docker/daemon.json 2>/dev/null || true)" != "$daemon_before" ]; then
        systemctl restart docker >/dev/null 2>&1 || true
      fi
    elif command -v sudo >/dev/null 2>&1; then
      if command -v jq >/dev/null 2>&1; then
        sudo -n sh -c 'jq ".mtu = 1460" /etc/docker/daemon.json > /etc/docker/daemon.json.tmp && mv /etc/docker/daemon.json.tmp /etc/docker/daemon.json' 2>/dev/null || true
      else
        sudo -n sed -i 's/"mtu": *[0-9]*/"mtu": 1460/' /etc/docker/daemon.json 2>/dev/null || true
      fi
      if [ "$(cksum < /etc/docker/daemon.json 2>/dev/null || true)" != "$daemon_before" ]; then
        sudo -n systemctl restart docker >/dev/null 2>&1 || true
      fi
    fi
  fi

  if command -v iptables >/dev/null 2>&1; then
    if [ "$(id -u)" = 0 ]; then
      iptables -t mangle -C POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || \
        iptables -t mangle -A POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || true
    elif command -v sudo >/dev/null 2>&1; then
      sudo -n iptables -t mangle -C POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || \
        sudo -n iptables -t mangle -A POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || true
    fi
  fi
}

ensure_transparent_cache() {
  tune_host || return 0
  local host="172.17.0.1"
  local reachable=false
  if command -v nc >/dev/null 2>&1; then
    if nc -z -w 1 "$host" 53 2>/dev/null; then
      reachable=true
    fi
  elif command -v python3 >/dev/null 2>&1; then
    if python3 -c "
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(1)
try:
    s.connect(('$host', 53))
except OSError:
    sys.exit(1)
" 2>/dev/null; then
      reachable=true
    fi
  elif [ -x /usr/bin/timeout ]; then
    if /usr/bin/timeout 1 /bin/bash -c "echo >/dev/tcp/$host/53" 2>/dev/null; then
      reachable=true
    fi
  elif (echo >/dev/tcp/"$host"/53) 2>/dev/null; then
    reachable=true
  fi
  if [ "$reachable" != true ]; then
    return 0
  fi

  # 1. Install Root CA if missing
  if [ ! -f /usr/local/share/ca-certificates/grain-ca.crt ] && command -v openssl >/dev/null 2>&1; then
    local ca_cert
    ca_cert="$(openssl s_client -showcerts -servername ghcr.io -connect "$host:443" </dev/null 2>/dev/null | awk '/BEGIN CERTIFICATE/{n++} n==2; /END CERTIFICATE/{if(n==2) exit}')" || ca_cert=""
    if [ -n "$ca_cert" ]; then
      if [ "$(id -u)" = 0 ]; then
        mkdir -p /usr/local/share/ca-certificates
        printf '%s\n' "$ca_cert" > /usr/local/share/ca-certificates/grain-ca.crt
        update-ca-certificates >/dev/null 2>&1 || true
      elif command -v sudo >/dev/null 2>&1; then
        sudo -n mkdir -p /usr/local/share/ca-certificates 2>/dev/null || true
        printf '%s\n' "$ca_cert" | sudo -n tee /usr/local/share/ca-certificates/grain-ca.crt >/dev/null 2>&1 || true
        sudo -n update-ca-certificates >/dev/null 2>&1 || true
      fi
    fi
  fi

  # 2. Point DNS at the transparent cache resolver if not already pointed
  if ! grep -q "$host" /etc/resolv.conf 2>/dev/null; then
    if [ "$(id -u)" = 0 ]; then
      printf 'nameserver %s\n' "$host" > /etc/resolv.conf 2>/dev/null || true
    elif command -v sudo >/dev/null 2>&1; then
      printf 'nameserver %s\n' "$host" | sudo -n tee /etc/resolv.conf >/dev/null 2>&1 || true
    fi
  fi
}
