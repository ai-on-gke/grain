#!/usr/bin/env bash
# granule-diag.sh -- bisect "grain's granule sandbox VM is up but its guest is
# not reachable" down to the single hop that is actually broken.
#
# Reaching a granule sandbox VM under the docker backend (the default, see
# pkg/cli/daemon.go's -granule-backend) differs by netMode
# (orchestrator.GranuleConfig.NetMode, "flat" by default since
# bwsalmon/agents#577's own predecessor -- see 7a58bec):
#
#   flat (default): SetupFlat splices the guest directly onto the netns
#     container's own network identity -- from outside that netns, the
#     guest simply *is* the pod IP, sshd included, with no NAT of any kind.
#     A second, private link (netshim's "control" bridge/tap) is what
#     `granule exec` actually dials the guest over, since the netns's own
#     stack can no longer reach the address the splice stole from it.
#
#   nat: the original mode -- a private subnet inside the netns, and
#     netshim's own nftables DNAT rules forward <pod IP>:<port> to
#     <guest IP>:<guestPort>, chained as:
#       host -> netns container's docker IP ("the pod IP")
#            -> netshim's nftables DNAT
#            -> the tap device / bridge cloud-hypervisor attached the guest to
#            -> the guest's own eth0, addressed from the "ip=" kernel cmdline
#            -> sshd listening inside the guest
#
# Either way, grain itself never dials the guest directly (bwsalmon/granule
# commit caea585, "Reach a granule sandbox guest over docker exec, not a
# forwarded port"): pkg/mcp/docker_exec_runner.go always runs
# `docker exec <container> granule exec --`, and it is `granule exec` inside
# that container, not grain, that finds the guest -- over the control link
# in flat mode, over the guest's own address in NAT mode. So the decisive
# "is this actually reachable the way grain reaches it" check here is that
# same `docker exec ... granule exec` invocation, not a raw ssh dial -- see
# "the way grain does it" below.
#
# This script walks the hops for whichever mode the VM's own saved spec
# names, in order, and reports the first one that fails, with the raw
# evidence underneath, so the next step is a fact rather than a guess.
#
# The last hop goes the other way: whether the guest can reach anything
# *out*. That one is here because it is the only failure in this file
# that hides behind a healthy VM -- a guest with no default route passes
# every check above it and still leaves its run with no network (see
# "Hop 7" below; there is no VENDORED.md in third_party/granule and the
# guest-side fix for that failure is granule's own now, so
# scripts/granule/README.md's "Two guest fixes that left with the
# vendored kontur tree" is where the account of it lives).
#
# Everything here is read-only: it inspects state, containers, the netns
# and the guest's console log, and opens TCP connections (and, for the
# final check, execs into the VM's own container the same way grain does).
# It changes nothing. Run it as root on the host running the sandbox VMs.
#
# Usage:
#   sudo scripts/granule-diag.sh [vm-name]
#
# With no vm-name it diagnoses every VM in the state directory. Override
# the state directory with GRANULE_STATE_DIR (default /var/lib/granule/vms,
# matching pkg/granule.DefaultStateDir), and the account the final
# end-to-end check logs in as with GRANULE_SSH_USER (the same value
# grain's own -granule-ssh-user carries). GRANULE_SSH_KEY is optional and
# normally unset: granule generates a keypair for each guest it boots and
# leaves it at `granule exec`'s own default path inside the VM container,
# so the check needs no identity of its own. Set it only against a custom
# guest image that authorizes a key of its own instead.

set -uo pipefail

STATE_DIR="${GRANULE_STATE_DIR:-/var/lib/granule/vms}"
SSH_KEY="${GRANULE_SSH_KEY:-}"
SSH_USER="${GRANULE_SSH_USER:-debian}"
CONNECT_TIMEOUT="${GRANULE_CONNECT_TIMEOUT:-5}"

fail_count=0

say()  { printf '%s\n' "$*"; }
hdr()  { printf '\n=== %s ===\n' "$*"; }
ok()   { printf '  [ OK ] %s\n' "$*"; }
bad()  { printf '  [FAIL] %s\n' "$*"; fail_count=$((fail_count + 1)); }
warn() { printf '  [WARN] %s\n' "$*"; }
info() { printf '         %s\n' "$*"; }

need() {
  command -v "$1" >/dev/null 2>&1 || { say "granule-diag: required tool '$1' is not installed"; exit 2; }
}
have() { command -v "$1" >/dev/null 2>&1; }

need docker
have jq || { say "granule-diag: 'jq' is required to read granule's state JSON"; exit 2; }

# tcp_connect HOST PORT -- succeeds if a TCP connection is established
# within CONNECT_TIMEOUT. Uses bash's own /dev/tcp rather than nc, which
# is not installed on every host and, more importantly, is not needed:
# this has to work under `nsenter -n` too, where only the network
# namespace changes and the binary still comes from the host.
tcp_connect() {
  timeout "$CONNECT_TIMEOUT" bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null
}

# in_netns PID CMD... -- run a host binary inside the netns container's
# network namespace.
in_netns() { nsenter -t "$1" -n "${@:2}"; }

diagnose() {
  local name="$1"
  local spec="$STATE_DIR/$name.json"

  hdr "VM $name"

  # ---- Hop 0: granule's own saved spec -----------------------------------
  if [ ! -f "$spec" ]; then
    bad "no saved spec at $spec"
    return
  fi
  local ip port guest_port backend cmdline cmdline_auto net_mode
  ip="$(jq -r '.ip // ""' "$spec")"
  port="$(jq -r '.port // 0' "$spec")"
  guest_port="$(jq -r '.guestPort // 0' "$spec")"
  backend="$(jq -r '.backend // "static-pod"' "$spec")"
  cmdline="$(jq -r '.cmdline // ""' "$spec")"
  cmdline_auto="$(jq -r '.cmdlineAuto // false' "$spec")"
  net_mode="$(jq -r '.netMode // "nat"' "$spec")"
  say "  spec: ip=$ip port=$port guestPort=$guest_port backend=$backend netMode=$net_mode"
  say "  cmdline: ${cmdline:-<none>}${cmdline_auto:+ (cmdlineAuto=$cmdline_auto)}"

  if [ "$backend" != "docker" ]; then
    warn "backend is $backend, not docker -- this script only diagnoses the docker backend"
    return
  fi

  if [ "$net_mode" = "flat" ]; then
    # grain never re-creates a VM whose state file it can already read
    # (orchestrator.GranuleSandboxes.ensure treats that as "already
    # exists"), so ip/port/guestPort below are whatever they were the day
    # this VM was first created. In flat mode none of the three are ever
    # meaningful: SetupFlat's own doc comment says why -- "it never writes
    # net.ipv4.ip_forward ... and it installs no nftables rules (there is
    # no NAT)". The guest simply *is* the pod IP, sshd on 22, no
    # translation involved -- scripts/setup.sh's write_systemd_units
    # correctly skips -granule-create-arg=-guest-port=22 under flat mode
    # for exactly this reason, so seeing guestPort=80 here is normal, not
    # a bug.
    info "flat mode: ip/port/guestPort above are unused -- the guest takes over"
    info "the pod's own address wholesale (SetupFlat) and listens on 22 directly."
  else
    if [ "$guest_port" = "22" ]; then
      ok "guestPort is 22 (the port the guest image's sshd actually listens on)"
    else
      bad "guestPort is $guest_port, not 22 -- netshim DNATs $port to $ip:$guest_port, where nothing is listening"
      info "granule's own default guestPort is 80. grain must pass"
      info "  -granule-create-arg=-guest-port -granule-create-arg=22"
      info "and this VM predates that (grain reuses any VM whose state file exists"
      info "rather than re-creating it -- see GranuleSandboxes.ensure). Fix by deleting"
      info "and letting grain recreate it:  granule vm delete $name -state-dir $STATE_DIR"
    fi
  fi
  if [ "$cmdline_auto" = "true" ]; then
    info "cmdlineAuto=true -- the ip=/eth0 granule actually boots with is derived at container"
    info "start (DiscoverIdentity) and never written back to this spec file; see this VM's"
    info "own guest console markers section below for the cmdline it really used."
  else
    case "$cmdline" in
      *:eth0:*) ok "cmdline names eth0 (the name the guest image pins via 00-eth0.link)" ;;
      *)        warn "cmdline does not name eth0 -- ipconfig(8) in the guest configures nothing" ;;
    esac
  fi

  # ---- Hop 1: the container(s) ------------------------------------------
  local vm_c netns_c
  vm_c="granule-vm-$name"
  netns_c="granule-vm-$name-netns"
  local vm_status netns_status
  vm_status="$(docker inspect -f '{{.State.Status}}' "$vm_c" 2>/dev/null)"
  netns_status="$(docker inspect -f '{{.State.Status}}' "$netns_c" 2>/dev/null)"
  local holder_c="$vm_c"
  if [ "$netns_status" = "running" ]; then
    holder_c="$netns_c"
    ok "netns holder $netns_c is running"
  elif [ "$vm_status" = "running" ]; then
    ok "VM container $vm_c is running (unified netns)"
  fi
  if [ "$vm_status" != "running" ]; then
    bad "VM container $vm_c is ${vm_status:-absent} (exit $(docker inspect -f '{{.State.ExitCode}}' "$vm_c" 2>/dev/null))"
    info "last console output:"
    docker logs --tail 25 "$vm_c" 2>&1 | sed 's/^/           /'
    return
  fi

  local pid
  pid="$(docker inspect -f '{{.State.Pid}}' "$holder_c" 2>/dev/null)"
  if [ -z "$pid" ] || [ "$pid" = "0" ]; then
    bad "cannot resolve $holder_c's PID to enter its network namespace"
    return
  fi

  # ---- Hop 2: the shared network namespace ------------------------------
  local pod_ip tap ctl_tap
  pod_ip="$(docker inspect -f '{{$i := index .NetworkSettings "IPAddress"}}{{if $i}}{{$i}}{{else}}{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}{{end}}' "$holder_c" 2>/dev/null)"
  tap="tap-$name"
  ctl_tap="ctl-$name"
  say "  pod IP: ${pod_ip:-<none>}"
  info "links in the shared netns:"
  in_netns "$pid" ip -br addr 2>&1 | sed 's/^/           /'

  # cmdline's ip= should carry whatever address the guest is actually
  # meant to end up with: the pod IP in flat mode (SetupFlat hands the
  # guest the namespace's own identity), the spec's own "ip" field in NAT
  # mode (netshim's private subnet address). Skipped when cmdlineAuto is
  # true -- see the note above this spec's own "cmdline" field never
  # carries the real one in that case.
  if [ "$cmdline_auto" != "true" ]; then
    local expect_ip
    if [ "$net_mode" = "flat" ]; then expect_ip="$pod_ip"; else expect_ip="$ip"; fi
    case "$cmdline" in
      *"ip=$expect_ip::"*) ok "cmdline carries the guest's own static address (ip=$expect_ip::...)" ;;
      "")                  bad "cmdline is empty -- the guest gets no ip= and will never address eth0" ;;
      *)                   warn "cmdline's ip= does not match $expect_ip -- guest and its expected address may disagree" ;;
    esac
  fi

  if in_netns "$pid" ip link show granule0 >/dev/null 2>&1; then
    ok "netshim's bridge granule0 exists"
  else
    bad "no granule0 bridge in the netns -- netshim never ran, or ran against a different namespace"
  fi

  # A tap device reports NO-CARRIER until something opens its file
  # descriptor. cloud-hypervisor holding it open is precisely what makes
  # the guest reachable, so this is the cheapest proof that the VMM
  # actually attached to the tap netshim built for it.
  if in_netns "$pid" ip link show "$tap" >/dev/null 2>&1; then
    local tapline
    tapline="$(in_netns "$pid" ip -br link show "$tap" 2>/dev/null)"
    if printf '%s' "$tapline" | grep -q 'NO-CARRIER'; then
      bad "$tap has NO-CARRIER -- cloud-hypervisor is not attached to it"
      info "$tapline"
      info "the guest has no link at all; check the VM container's console log below"
    else
      ok "$tap is up with carrier (cloud-hypervisor is attached)"
    fi
    local master
    master="$(in_netns "$pid" ip -o link show "$tap" 2>/dev/null | grep -o 'master [^ ]*' | awk '{print $2}')"
    if [ "$net_mode" = "flat" ]; then
      # SetupFlat's own doc comment: this tap's only peer is the splice,
      # not a bridge -- "unlike ensureTap it attaches the tap to no
      # bridge". A master here would mean the splice never happened.
      if [ -z "$master" ]; then
        ok "$tap has no master (spliced directly onto the external interface, as flat mode expects)"
      else
        bad "$tap's master is $master -- flat mode expects no master at all (splice(), not a bridge)"
      fi
    elif [ "$master" = "granule0" ]; then
      ok "$tap is enslaved to granule0"
    else
      bad "$tap's master is ${master:-<none>}, not granule0 -- guest traffic never reaches the bridge"
    fi
  else
    bad "no $tap device in the netns -- netshim did not create this VM's tap"
  fi

  if [ "$net_mode" = "flat" ]; then
    if in_netns "$pid" ip link show "$ctl_tap" >/dev/null 2>&1; then
      local ctl_master
      ctl_master="$(in_netns "$pid" ip -o link show "$ctl_tap" 2>/dev/null | grep -o 'master [^ ]*' | awk '{print $2}')"
      if [ "$ctl_master" = "granule0" ]; then
        ok "$ctl_tap (the control link) is enslaved to granule0 -- this is what 'granule exec' dials over"
      else
        bad "$ctl_tap's master is ${ctl_master:-<none>}, not granule0 -- 'granule exec' has no path to the guest"
      fi
    else
      bad "no $ctl_tap device -- no control link, so 'granule exec' and the memory agent cannot reach this guest"
    fi
  fi

  # ---- Hop 3: netshim's DNAT rules (NAT mode only) -----------------------
  if [ "$net_mode" = "flat" ]; then
    info "flat mode installs no nftables rules (SetupFlat's own doc comment: \"there is no NAT\") -- skipping"
  elif have nft; then
    local rules
    rules="$(in_netns "$pid" nft list table ip granule 2>&1)"
    if printf '%s' "$rules" | grep -q 'chain prerouting'; then
      ok "netshim's nftables table 'granule' is installed"
      if printf '%s' "$rules" | grep -q "dnat to $ip:$guest_port"; then
        ok "DNAT rule targets $ip:$guest_port"
      else
        bad "no 'dnat to $ip:$guest_port' rule -- the forward does not point where the spec says"
      fi
      if printf '%s' "$rules" | grep -q "dport $port"; then
        ok "a DNAT rule matches dport $port"
      else
        bad "no rule matching dport $port -- nothing forwards the port grain dials"
      fi
      # The prerouting rule additionally matches the pod IP netshim saw on
      # eth0 when it ran. Docker hands out a new IP on every container
      # recreate, so a stale rule silently matches nothing.
      if [ -n "$pod_ip" ] && printf '%s' "$rules" | grep -q "daddr $pod_ip"; then
        ok "DNAT matches the netns container's current IP ($pod_ip)"
      elif [ -n "$pod_ip" ]; then
        bad "DNAT matches a different address than $pod_ip -- netshim ran against a since-replaced container IP"
        info "$(printf '%s' "$rules" | grep -o 'daddr [0-9.]*' | sort -u | tr '\n' ' ')"
      fi
    else
      bad "no nftables table 'granule' in the netns -- netshim's rules are missing entirely"
      info "$rules"
    fi
  else
    warn "'nft' is not installed on the host -- skipping the DNAT rule check"
  fi

  # ---- Hop 4: the guest itself, off the network --------------------------
  if [ "$net_mode" = "flat" ]; then
    # Pinging or dialing $pod_ip from *inside* this same netns does not
    # work by design, not because anything is broken: SetupFlat's own doc
    # comment says the splice "steals the interface's ingress, so the
    # namespace's own stack can never receive a reply" at that address --
    # the namespace's own stack still thinks it owns $pod_ip, so a socket
    # opened in here never reaches the guest at all. Testing has to
    # happen from the host, the same side grain itself sits on.
    if tcp_connect "${pod_ip:-}" 22; then
      ok "sshd answers at $pod_ip:22 from the host (guest networking and sshd are both fine)"
    else
      bad "no TCP answer at $pod_ip:22 from the host"
      info "check the guest's own boot: docker logs $vm_c 2>&1 | grep -i -e ipconfig -e eth0 -e granule-net -e sshd"
    fi
    info "(a ping/connect to $pod_ip from *inside* this netns would fail even on a healthy"
    info "guest -- SetupFlat's splice steals that address's ingress; see this script's own"
    info "header. This is why the check above runs from the host instead.)"
  else
    # This is the decisive split for NAT mode. Reaching the guest directly
    # at its bridge address bypasses every DNAT rule above: if this works
    # and the pod-IP check below does not, the bug is in netshim's rules;
    # if this fails too, the guest's own networking or sshd never came up
    # and the rules are irrelevant.
    if in_netns "$pid" ping -c 2 -W 2 "$ip" >/dev/null 2>&1; then
      ok "guest answers ICMP at $ip (its eth0 is configured)"
    else
      bad "guest does not answer ping at $ip -- its eth0 never got an address"
      info "the guest image configures eth0 from the ip= cmdline via the"
      info "granule-net-cmdline.service oneshot (granule's guest overlay, third_party/granule/deploy/guest-image)."
      info "grep the console log for it:  docker logs $vm_c 2>&1 | grep -i -e ipconfig -e eth0 -e granule-net"
    fi

    if in_netns "$pid" timeout "$CONNECT_TIMEOUT" bash -c "exec 3<>/dev/tcp/$ip/$guest_port" 2>/dev/null; then
      ok "sshd answers directly at $ip:$guest_port (guest networking and sshd are both fine)"
    else
      bad "no TCP answer at $ip:$guest_port straight off the bridge"
      info "if ping above succeeded, the guest is addressed but sshd is not listening;"
      info "if ping failed too, the guest has no working network at all."
    fi

    # ---- Hop 5: through the DNAT, from inside then outside the netns ----
    # netshim installs the same DNAT in both prerouting and output, because
    # prerouting never sees traffic originated inside the pod's own netns.
    # Testing both separates a broken output rule from a broken prerouting
    # rule -- they fail independently.
    if [ -n "$pod_ip" ]; then
      if in_netns "$pid" timeout "$CONNECT_TIMEOUT" bash -c "exec 3<>/dev/tcp/$pod_ip/$port" 2>/dev/null; then
        ok "$pod_ip:$port answers from inside the netns (output-chain DNAT works)"
      else
        bad "$pod_ip:$port does not answer from inside the netns (output-chain DNAT)"
      fi
      if tcp_connect "$pod_ip" "$port"; then
        ok "$pod_ip:$port answers from the host (prerouting DNAT works)"
      else
        bad "$pod_ip:$port does not answer from the host (prerouting DNAT)"
      fi
    fi

    # Dialing port 22 on the pod IP is expected to fail and is not a bug:
    # netshim only ever forwards the VM's own external port, and the netns
    # holder itself runs "granule sleep", which listens on nothing.
    if [ -n "$pod_ip" ] && [ "$port" != "22" ]; then
      if tcp_connect "$pod_ip" 22; then
        warn "$pod_ip:22 answers -- unexpected, nothing in granule forwards port 22 on the pod IP"
      else
        info "note: $pod_ip:22 not answering is expected -- netshim forwards only port $port,"
        info "and the netns holder container itself listens on nothing. Dial $pod_ip:$port."
      fi
    fi
  fi

  # ---- Hop 6: end to end, the way grain actually does it -----------------
  # pkg/mcp/docker_exec_runner.go never dials the guest itself: every
  # run_command/read_file/edit_file/write_file call is
  # `docker exec <container> granule exec -- <argv>`, so this is that same
  # invocation rather than a raw ssh dial -- the latter tests a path
  # nothing in grain actually takes under the docker backend, in either
  # net mode.
  # GRANULE_EXEC_KEY is passed only when one was given: empty would name a
  # key file `granule exec` cannot open, rather than meaning "use your own
  # default" -- which is the whole point now that granule generates one per
  # boot and leaves it at exactly that default.
  local key_env=()
  if [ -n "$SSH_KEY" ]; then
    key_env=(-e "GRANULE_EXEC_KEY=$SSH_KEY")
  fi
  local out
  local guest_reachable=0
  if out="$(docker exec -e "GRANULE_EXEC_USER=$SSH_USER" "${key_env[@]}" \
      "$vm_c" granule exec -- whoami 2>&1)"; then
    ok "docker exec $vm_c granule exec -- whoami succeeds ($out) -- this VM is fully reachable the way grain reaches it"
    guest_reachable=1
  else
    bad "docker exec $vm_c granule exec -- whoami failed"
    # SC2001 wants ${out//search/replace} here, which cannot express this:
    # the anchor is the start of *every* line of a captured multi-line
    # error, and bash's replacement has no per-line anchor. sed is also
    # what the three sibling indents in this file use, and they are piped
    # from commands rather than fed a variable, so only this one is
    # noticed at all.
    # shellcheck disable=SC2001
    sed 's/^/           /' <<<"$out"
  fi

  # ---- Hop 7: the guest's route *out* -----------------------------------
  # Every hop above is about reaching the guest. This one is the opposite
  # direction, and it is the one that fails silently: a guest with no
  # default route boots, answers sshd, and passes every check above, while
  # a run inside it cannot reach anything but its own subnet -- no package
  # registry, no GitHub, no apt mirror. (The git proxy still works, since
  # it lives on the gateway address itself, which is directly connected --
  # which is exactly why a dispatch looks healthy.)
  #
  # In flat mode the guest inherits the namespace's own default route, via
  # the gateway field of the ip= parameter netshim derives (its
  # DiscoverIdentity/FlatGuestConfig); in NAT mode granule fills that
  # field in from the bridge CIDR. Either way the guest ends up with a
  # default route or it has no egress at all, so that is what this reads.
  if [ "$guest_reachable" = "1" ]; then
    local guest_route netns_gw
    guest_route="$(docker exec -e "GRANULE_EXEC_USER=$SSH_USER" "${key_env[@]}" \
      "$vm_c" granule exec -- ip -4 route show default 2>/dev/null | tr -d '\r')"
    netns_gw="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}' "$netns_c" 2>/dev/null)"
    if [ -z "$guest_route" ]; then
      bad "the guest has no default route -- it can reach its own subnet and nothing else"
      info "its ip= boot parameter carries an empty gateway field; check it with:"
      info "  docker logs $vm_c 2>&1 | grep -o 'ip=[^ ]*'"
      info "a flat-mode gateway comes from netshim's DiscoverIdentity reading this"
      info "namespace's own default route (${netns_gw:-none reported by docker})."
    else
      ok "the guest has a default route: $guest_route"
      if [ "$net_mode" = "flat" ] && [ -n "$netns_gw" ] && ! printf '%s' "$guest_route" | grep -q "via $netns_gw"; then
        warn "it does not name $netns_gw, the gateway docker gave this VM's namespace --"
        warn "expected in flat mode, where the guest takes that namespace's identity over"
      fi
    fi
  fi

  # ---- Guest console: the evidence for every hop-4 failure --------------
  hdr "VM $name: guest console markers"
  local log
  log="$(docker logs "$vm_c" 2>&1)"
  for marker in 'granule-net-cmdline' 'ipconfig' 'eth0' 'sshd' 'Kernel panic' 'Cannot open root device'; do
    local hit
    hit="$(printf '%s\n' "$log" | grep -i -- "$marker" | tail -3)"
    if [ -n "$hit" ]; then
      say "  --- $marker ---"
      printf '%s\n' "$hit" | sed 's/^/      /'
    else
      say "  --- $marker: no match in console output ---"
    fi
  done
  say "  (full console: docker logs $vm_c)"
}

if [ "$#" -gt 0 ]; then
  for n in "$@"; do diagnose "$n"; done
else
  shopt -s nullglob
  specs=("$STATE_DIR"/*.json)
  if [ "${#specs[@]}" -eq 0 ]; then
    say "granule-diag: no VM specs in $STATE_DIR -- grain has not created a sandbox VM yet"
    exit 1
  fi
  for s in "${specs[@]}"; do
    n="$(basename "$s" .json)"
    diagnose "$n"
  done
fi

hdr "summary"
if [ "$fail_count" -eq 0 ]; then
  say "  no failures -- every hop from the host to the guest's sshd checks out"
else
  say "  $fail_count check(s) failed -- fix the first [FAIL] above; the later ones are usually downstream of it"
fi
exit $(( fail_count > 0 ? 1 : 0 ))
