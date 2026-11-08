#!/bin/sh
# Prepare an isolated host HOME for the 2026-11-07 hosts walkthrough.
#
#   setup-host.sh <name> [logins]
#
# Creates /tmp/host-<name>/home with a stub `claude` agent CLI on PATH and,
# when logins is "logins", the files pkg/hostd/logins.go detects as an
# ambient claude and codex login. For a full `grain host` it also writes a
# root-owned allowlist (/tmp/host-<name>/hostd.yaml, sudo) offering that
# CLI plus one capability server, so a capability server can be placed on
# the host; grain-agentd keeps its own user-owned agents-only allowlist.
set -eu
name=$1
logins=${2:-}
root=/tmp/host-$name
home=$root/home
mkdir -p "$home/bin" "$home/.grain/hostd-stage" "$root/state"
cat >"$home/bin/claude" <<'EOF'
#!/bin/sh
# Walkthrough stub agent CLI: answers --version and echoes what it ran.
case "${1:-}" in
--version|-v) echo "0.0.0 (walkthrough stub claude)"; exit 0 ;;
esac
echo "stub claude ran on $(hostname) as $(id -un) in $(pwd): $*"
EOF
chmod 755 "$home/bin/claude"
if [ "$logins" = logins ]; then
	mkdir -p "$home/.claude" "$home/.codex"
	echo '{}' >"$home/.claude/.credentials.json"
	echo '{}' >"$home/.codex/auth.json"
fi
cat >"$root/hostd.yaml.new" <<EOF
labels:
  zone: walk-$name
capacity: 2
clis:
  claude:
    path: $home/bin/claude
    cwd_under: [$home/.grain/hostd-stage, /tmp]
capabilities:
  walk-echo:
    command: /bin/cat
EOF
sudo install -o root -g root -m 0644 "$root/hostd.yaml.new" "$root/hostd.yaml"
rm -f "$root/hostd.yaml.new"
echo "$root ready"
