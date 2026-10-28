#!/usr/bin/env bash
#
# The granule demo: the operations this project claims, run in front of an
# audience one line at a time.
#
# Each step prints the command it is about to run, waits for a keypress,
# runs it, and prints what came back with its exit status and how long it
# took. Nothing is hidden behind a helper function -- every line shown is
# the line executed, so an attendee can copy any of them out of the
# recording and have it work. That is the whole design constraint: a demo
# whose script does something other than what it shows is a demo that
# teaches the wrong thing.
#
# Run `demo/demo.sh --help` for the chapters and the flags, and read
# demo/README.md for what each chapter is demonstrating and what it needs.
#
# Rehearse with `demo/demo.sh --dry-run`, which prints every line of every
# chapter and runs none of them. That mode needs no docker, no KVM, no
# registry credential and no cluster, so it is also what CI can exercise:
# it is the check that the chapters still parse, that the assets are still
# where the script says and that no line has lost its quoting.

set -euo pipefail

# ---------------------------------------------------------------------------
# Where things are
# ---------------------------------------------------------------------------

demo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$demo_dir/.." && pwd)
assets=$demo_dir/assets

# Everything this demo creates on the local machine goes under one
# directory, so that `demo.sh cleanup` is a directory removal plus the
# containers, and so that a demo run cannot disturb VMs the operator of
# this machine created for real.
DEMO_STATE=${DEMO_STATE:-$repo_root/.demo}

# granule's own state directory follows XDG when it is not running as
# root (pkg/cli/statedir.go), so pointing XDG_STATE_HOME here is
# enough to keep the demo's VMs out of ~/.local/state and out of
# /var/lib/granule. It also means no command below has to carry a
# -state-dir flag, which keeps the lines short enough to read from the
# back of a room.
export XDG_STATE_HOME=$DEMO_STATE/state

# The demo runs this tree's CLI, not whatever is installed: the point of
# a demo is the code in front of you. Putting it on PATH rather than
# calling it by path is what lets every line below say `granule`.
export PATH=$DEMO_STATE/bin:$PATH

# ---------------------------------------------------------------------------
# Knobs. Every one of these is overridable from the environment, and the
# `provision` chapter prints the lot so a recording says what it ran with.
# ---------------------------------------------------------------------------

REGISTRY=${REGISTRY:-ghcr.io}
REPO=${REPO:-gke-labs/granule}
IMAGE_TAG=${IMAGE_TAG:-latest}

# The two images the publish produces and the rest of the demo consumes:
# the bootable one, and the guest rootfs a consumer writes `FROM`.
IMAGE=${IMAGE:-$REGISTRY/$REPO/debian:$IMAGE_TAG}
ROOTFS_IMAGE=${ROOTFS_IMAGE:-$REGISTRY/$REPO/debian.rootfs:$IMAGE_TAG}

# Built from this tree in `provision`. LOCAL_IMAGE is the whole thing;
# RUNTIME_IMAGE is the VMM half alone (`--target runtime`).
LOCAL_IMAGE=${LOCAL_IMAGE:-granule:latest}
RUNTIME_IMAGE=${RUNTIME_IMAGE:-granule:runtime}

# The image supplying the VMM half a pack or a rebase builds onto
# (-kontur-image). The full image, because this demo's guest is the
# reference one and brings no kernel of its own: the `--target runtime`
# stage carries no `vmlinux` either -- deliberately, it is the half for
# guests that bring their own -- so packing this guest onto it is
# refused, at build time, rather than producing an image that fails at
# boot. The price paid instead is the full image's bundled guest disk in
# the packed result's lower layers, which `guest pack` reports on its
# way out.
#
# A deployment whose guest brings a kernel (a rootfs built with
# GUEST_KERNEL_PACKAGE -- the debian12 variant) can point this at
# $RUNTIME_IMAGE and stop paying for two guests.
VMM_IMAGE=${VMM_IMAGE:-$LOCAL_IMAGE}

# Images the build/rebase chapters produce.
GUEST_IMAGE=${GUEST_IMAGE:-granule-demo-guest:latest}
GUEST_ROOTFS=${GUEST_ROOTFS:-granule-demo-guest:rootfs}
PACKED_IMAGE=${PACKED_IMAGE:-granule-demo-packed:latest}
REBASED_IMAGE=${REBASED_IMAGE:-granule-demo-reb:latest}
REBASED_FULL_IMAGE=${REBASED_FULL_IMAGE:-granule-demo-reb:full}

# VM and pod names.
VM_LOCAL=${VM_LOCAL:-demo-web}
VM_ONESHOT=${VM_ONESHOT:-demo-once}
POD_SINGLE=${POD_SINGLE:-granule-gke-exec-example}
POD_MULTI=${POD_MULTI:-granule-multi-vm}
EPHEMERAL_VM=${EPHEMERAL_VM:-vm-injected}

# GKE. GKE_PROJECT has no default on purpose: a demo that guesses which
# project to create a cluster in is a demo that bills somebody.
GKE_PROJECT=${GKE_PROJECT:-}
GKE_CLUSTER=${GKE_CLUSTER:-granule-demo}
GKE_ZONE=${GKE_ZONE:-us-central1-a}
GKE_MACHINE=${GKE_MACHINE:-n2-standard-4}
GKE_NODES=${GKE_NODES:-1}

# GHCR credentials for the publish chapter. GHCR_USER defaults to the
# GitHub username docker is already logged in as, if any.
GHCR_USER=${GHCR_USER:-${GITHUB_USER:-}}
GHCR_TOKEN=${GHCR_TOKEN:-${GITHUB_TOKEN:-}}

# ---------------------------------------------------------------------------
# Presentation
# ---------------------------------------------------------------------------

mode=interactive   # interactive | auto | dry-run
delay=${DEMO_DELAY:-2}
keep_going=false
use_color=auto
step_no=0
chapter_name=-
failures=0
skipped=0

if [ -t 1 ] && [ "${NO_COLOR:-}" = "" ]; then use_color=true; else use_color=false; fi

set_colors() {
  if [ "$use_color" = true ]; then
    c_off=$'\033[0m'; c_rule=$'\033[38;5;240m'; c_head=$'\033[1;36m'
    c_cmd=$'\033[1;33m'; c_note=$'\033[38;5;245m'; c_ok=$'\033[1;32m'
    c_bad=$'\033[1;31m'; c_skip=$'\033[1;35m'
  else
    c_off=; c_rule=; c_head=; c_cmd=; c_note=; c_ok=; c_bad=; c_skip=
  fi
}

rule() { printf '%s%s%s\n' "$c_rule" "$(printf '─%.0s' $(seq 1 "${COLUMNS:-78}"))" "$c_off"; }

# chapter announces a section. Every chapter function calls this first,
# so that a run of one chapter and a run of all of them look the same.
chapter() {
  chapter_name=$1
  printf '\n\n%s╔══ %s ══%s\n' "$c_head" "$1" "$c_off"
  shift
  for line in "$@"; do printf '%s║  %s%s\n' "$c_head" "$line" "$c_off"; done
  printf '%s╚%s\n' "$c_head" "$c_off"
}

# note is narration: what the next line is for, in the presenter's words.
# It is printed but never executed, and it is deliberately separate from
# the shell comments in the commands themselves -- a comment scrolls past
# in the output, a note does not.
note() {
  local line
  for line in "$@"; do printf '%s# %s%s\n' "$c_note" "$line" "$c_off"; done
}

# pause waits for the audience between steps. --auto sleeps instead, and
# a non-terminal stdin (a recording harness, CI) implies --auto.
pause() {
  case $mode in
    interactive)
      if [ -t 0 ]; then
        printf '%s   [enter to run, s to skip, q to quit] %s' "$c_note" "$c_off"
        local key=
        IFS= read -r key || key=q
        case $key in
          q|Q) printf '\n%sstopping here%s\n' "$c_note" "$c_off"; exit 0 ;;
          s|S) return 1 ;;
        esac
      else
        sleep "$delay"
      fi
      ;;
    auto) sleep "$delay" ;;
    dry-run) : ;;
  esac
  return 0
}

# run is the whole demo engine: show the line, run the line, show the
# result. Options come before the command string.
#
#   -f  this command is allowed to fail -- it is being shown *because*
#       it fails (a refused rebase, a contract mismatch), so a non-zero
#       status is the demonstration rather than a broken demo.
#   -q  this command's output is a build log. It is written to a file
#       under $DEMO_STATE/logs instead of the screen, a heartbeat line
#       every ten seconds says how long it has been going and what it
#       last printed, and the last twenty lines are shown when it
#       finishes. Four thousand lines of debootstrap is not a demo, and
#       neither is a step that prints nothing for half an hour.
run() {
  local allow_fail=false quiet=false
  while [ $# -gt 1 ]; do
    case $1 in
      -f) allow_fail=true; shift ;;
      -q) quiet=true; shift ;;
      *) break ;;
    esac
  done
  local cmd=$1

  step_no=$((step_no + 1))
  rule
  printf '%s[%s %02d]%s %s$ %s%s\n' "$c_head" "$chapter_name" "$step_no" "$c_off" "$c_cmd" "$cmd" "$c_off"

  if ! pause; then
    printf '%s   skipped%s\n' "$c_skip" "$c_off"
    skipped=$((skipped + 1))
    return 0
  fi

  if [ "$mode" = dry-run ]; then
    printf '%s   (dry run: shown, not executed)%s\n' "$c_note" "$c_off"
    return 0
  fi

  local start=$SECONDS rc=0
  set +e
  if [ "$quiet" = true ]; then
    mkdir -p "$DEMO_STATE/logs"
    local log=$DEMO_STATE/logs/$chapter_name-$step_no.log
    eval "$cmd" >"$log" 2>&1 &
    local pid=$!
    # A build that prints nothing for twenty minutes looks like a demo
    # that has hung, and the audience is right to think so. The
    # heartbeat is the cheapest thing that is not that: how long, and
    # the line it last printed.
    local ticks=0
    while kill -0 "$pid" 2>/dev/null; do
      sleep 1
      ticks=$((ticks + 1))
      if [ $((ticks % 10)) -eq 0 ] && kill -0 "$pid" 2>/dev/null; then
        printf '%s    … %ss  %s%s\n' "$c_note" "$((SECONDS - start))" \
          "$(tail -n 1 "$log" 2>/dev/null | tr -d '\r' | cut -c1-72)" "$c_off"
      fi
    done
    wait "$pid"
    rc=$?
    printf '%s    (full output: %s)%s\n' "$c_note" "$log" "$c_off"
    tail -n 20 "$log" | sed 's/^/    /'
  else
    eval "$cmd" 2>&1 | sed 's/^/    /'
    rc=${PIPESTATUS[0]}
  fi
  set -e
  local elapsed=$((SECONDS - start))

  if [ "$rc" -eq 0 ]; then
    printf '%s   ✔ exit 0%s (%ss)\n' "$c_ok" "$c_off" "$elapsed"
    return 0
  fi
  if [ "$allow_fail" = true ]; then
    printf '%s   ✔ exit %d -- which is the point of this step%s (%ss)\n' "$c_ok" "$rc" "$c_off" "$elapsed"
    return 0
  fi

  printf '%s   ✘ exit %d%s (%ss)\n' "$c_bad" "$rc" "$c_off" "$elapsed"
  failures=$((failures + 1))
  if [ "$keep_going" != true ]; then
    printf '%sstopping: rerun with --keep-going to carry on past a failed step%s\n' "$c_bad" "$c_off"
    exit 1
  fi
  return 0
}

# skip_chapter bows out of a chapter whose prerequisites are not here,
# saying which one, rather than failing in the middle of it. In dry-run
# it never fires: a rehearsal shows every line whatever the machine has.
skip_chapter() {
  if [ "$mode" = dry-run ]; then return 1; fi
  printf '%s   ⊘ skipping %s: %s%s\n' "$c_skip" "$chapter_name" "$1" "$c_off"
  skipped=$((skipped + 1))
  return 0
}

have() { command -v "$1" >/dev/null 2>&1; }

# have_image is "this daemon can get at that reference", by inspect and
# then by pull. It is a prerequisite check rather than a step: a chapter
# whose first line creates a VM from an image nobody published should say
# so once, not fail that line and then fail the twelve that depended on
# it.
have_image() {
  docker image inspect "$1" >/dev/null 2>&1 && return 0
  docker pull -q "$1" >/dev/null 2>&1
}

# have_cluster is the same for kubectl: a configured context that answers.
have_cluster() { kubectl cluster-info >/dev/null 2>&1; }

# ---------------------------------------------------------------------------
# Chapter 1: provision
#
# What the rest of the demo needs, made in front of the audience rather
# than assumed. It builds this tree twice: the whole image (which is what
# gets published and what boots) and the runtime target alone (the VMM
# half, which is what a pack or a rebase builds onto).
# ---------------------------------------------------------------------------

chapter_provision() {
  chapter provision \
    "Prerequisites, this tree's granule CLI, and the two images" \
    "everything below is built from: the full one and the VMM half."

  note "Where this run will put everything it makes, and what it will call it." \
       "Every one of these is an environment variable you can override."
  run "printf '%s\n' \
  'repo root:     $repo_root' \
  'demo state:    $DEMO_STATE' \
  'published:     $IMAGE' \
  'guest rootfs:  $ROOTFS_IMAGE' \
  'local build:   $LOCAL_IMAGE (VMM half: $RUNTIME_IMAGE)' \
  'GKE:           ${GKE_PROJECT:-<unset: GKE chapters will skip>} / $GKE_CLUSTER / $GKE_ZONE'"

  note "A granule VM is a real cloud-hypervisor VM, so the host needs KVM." \
       "On a cloud VM that means nested virtualization is switched on."
  run "ls -l /dev/kvm"

  note "docker does the building and runs the VM containers."
  run "docker version --format 'client {{.Client.Version}} / server {{.Server.Version}}'"

  run "mkdir -p '$DEMO_STATE/bin' '$DEMO_STATE/state'"

  note "This tree's CLI, on PATH for the rest of the demo. Every 'granule'" \
       "below is this binary and not one somebody installed last month."
  run "go build -o '$DEMO_STATE/bin/granule' ./cmd/granule"
  run "granule --help"

  note "The whole image: runtime, cloud-hypervisor, the guest disk and the" \
       "kernel, in one artifact. This is the slow one -- it debootstraps a" \
       "guest rootfs -- so it is the thing to have pre-warmed before a demo."
  run -q "docker build -t '$LOCAL_IMAGE' ."

  note "The VMM half on its own: 28MB against 600, because it carries no" \
       "guest -- and no kernel either, which is the same decision. It is" \
       "what 'guest pack' and 'guest rebase' build onto for a guest that" \
       "brings its own kernel (a rootfs built with GUEST_KERNEL_PACKAGE)."
  run -q "docker build --target runtime -t '$RUNTIME_IMAGE' ."
  note "This demo's guest brings none, so the full image is what boots it" \
       "and -kontur-image below is $VMM_IMAGE. The price is its bundled" \
       "guest disk in the packed result's lower layers, which 'guest pack'" \
       "reports; packing onto the lean one is refused, at build time."

  run "docker images --format 'table {{.Repository}}:{{.Tag}}\t{{.Size}}' | grep -E 'granule|REPOSITORY'"
}

# ---------------------------------------------------------------------------
# Chapter 2: publish
#
# "Get granule pushing to GitHub": the images this project ships live in
# GHCR, and deploy/publish-images.sh is what puts them there. Nothing in
# CI publishes -- a person runs this with a credential -- so the demo is
# the honest version of the operation rather than a screenshot of a
# workflow.
# ---------------------------------------------------------------------------

chapter_publish() {
  chapter publish \
    "Pushing granule's images to the GitHub container registry, and" \
    "pulling one back down as any consumer would."

  if [ -z "$GHCR_TOKEN" ] && skip_chapter "no GHCR_TOKEN in the environment (a token with write:packages)"; then
    return 0
  fi
  if [ -z "$GHCR_USER" ] && skip_chapter "no GHCR_USER in the environment"; then
    return 0
  fi
  # publish-images.sh refuses a dirty tree, because the packed guests'
  # immutable <variant>-<commit> tags would then name a commit that did
  # not build the image (the rootfs's own tag is a hash of the tree, so
  # it has nothing to be wrong about). Its --dry-run only warns, so
  # without this check the demo
  # logs in, shows a dry run that looks fine, and dies on the line after
  # it. (The demo's own scratch directory is in .gitignore, so this is
  # about the presenter's edits and nothing the demo did.)
  if [ -n "$(git -C "$repo_root" status --porcelain 2>/dev/null)" ] &&
     skip_chapter "this tree has uncommitted changes and publish-images.sh will not publish from one -- commit them, or run the script by hand with --allow-dirty"; then
    return 0
  fi

  note "GHCR takes an ordinary docker login. The token needs write:packages," \
       "and read:packages on top of it if the package is private."
  run "printf '%s' \"\$GHCR_TOKEN\" | docker login $REGISTRY -u \"\$GHCR_USER\" --password-stdin"

  note "What the publish is going to do, before it does any of it. Two" \
       "images per guest -- the bootable one and the rootfs it was packed" \
       "from -- each under its moving tag, and each with an immutable one" \
       "beside it: <variant>-<commit> for the packed guest, which carries" \
       "granule's own half, and <variant>-os-<key> for the rootfs, which" \
       "is an operating system and is named after the recipe that built" \
       "it rather than after whatever granule did today."
  run "deploy/publish-images.sh --dry-run --no-login debian"

  note "And for real. It rebuilds from this tree rather than pushing what" \
       "the provision chapter built, so that what lands on GHCR is a" \
       "known commit and nothing else."
  run -q "deploy/publish-images.sh --no-login debian"

  note "Now pull it back the way anybody else would. A package GHCR made" \
       "on its first push is private: if this says 'denied', the package" \
       "needs making public in its GHCR settings, once, by hand."
  run "docker pull '$IMAGE'"
  run "docker pull '$ROOTFS_IMAGE'"

  note "The packed image declares which guest contract version it speaks." \
       "A rebase reads this label with one inspect and no disk opened -- see" \
       "the rebase chapter."
  run "docker image inspect -f '{{index .Config.Labels \"dev.granule.guest.contract\"}}' '$IMAGE'"
}

# ---------------------------------------------------------------------------
# Chapter 3: local
#
# A VM from the published image, on a laptop's docker daemon: create,
# exec into the guest, read the guest's console out of docker logs.
# ---------------------------------------------------------------------------

chapter_local() {
  chapter local \
    "One VM from the published image on a local docker daemon:" \
    "create, exec into the guest, read its console."

  if ! have docker && skip_chapter "no docker on PATH"; then return 0; fi
  if ! have_image "$IMAGE" &&
     skip_chapter "this daemon cannot get at $IMAGE -- run the publish chapter first, or set IMAGE= to something it has"; then
    return 0
  fi

  note "One command, one container, one VM. -kontur-image names the image" \
       "to boot -- here the one we just published -- and -wait holds until" \
       "the guest inside answers, not merely until the container is up."
  run "granule vm create $VM_LOCAL -kontur-image '$IMAGE' -cpus 2 -memory-mb 1024 -wait"

  note "granule knows about it; -status costs a docker call per VM and asks" \
       "the guest as well as the container."
  run "granule vm list -status"

  note "This is the difference that matters. The VM container is a scratch" \
       "image holding cloud-hypervisor and one Go binary: no shell, no" \
       "coreutils, none of the workload. docker exec's usual landing place" \
       "is not there to land on -- so this is supposed to fail."
  run -f "docker exec granule-vm-$VM_LOCAL cat /proc/version"

  note "'granule vm exec' lands inside the guest instead, over the VM's own" \
       "virtio-vsock device. Not IP: it reaches a guest whose network never" \
       "came up, or which has no NIC at all."
  run "granule vm exec $VM_LOCAL -- cat /proc/version"

  note "And because tooling that knows nothing about granule still reaches" \
       "for a shell, /bin/sh and /bin/bash in that container are symlinks to" \
       "the same binary, which forwards into the guest. So the habitual line" \
       "works, and what answers it is the machine inside."
  run "docker exec granule-vm-$VM_LOCAL sh -c 'cat /proc/version'"

  note "A different kernel, a different machine, its own root filesystem."
  run "granule vm exec $VM_LOCAL -- cat /etc/os-release"
  run "granule vm exec $VM_LOCAL -- sh -c 'grep MemTotal /proc/meminfo; grep -c ^processor /proc/cpuinfo'"

  note "The guest took over the container's IP, MAC and routes, so it is" \
       "addressed exactly as any other container on this daemon is."
  run "granule vm exec $VM_LOCAL -- ip -brief addr"
  run "granule vm exec $VM_LOCAL -- ip route"

  note "-w and -e describe the command rather than the connection, spelled" \
       "the way docker exec spells them."
  run "granule vm exec $VM_LOCAL -w /etc -e GREETING=hello -- sh -c 'pwd; echo \$GREETING; ls os-release'"

  note "And the logs: the guest's console is the VM container's stdout, so" \
       "kubectl logs and docker logs show a kernel booting."
  run "docker logs --tail 25 granule-vm-$VM_LOCAL 2>&1"

  note "Copying goes over that same vsock session, and is invoked the same" \
       "way -- 'docker cp' would reach the container, which holds two" \
       "binaries and none of the guest's filesystem. One end is always '-'," \
       "this container's stdin/stdout, which is what your shell redirects."
  run "date > '$DEMO_STATE/hello.txt' && docker exec -i granule-vm-$VM_LOCAL granule cp - /root/hello.txt < '$DEMO_STATE/hello.txt'"
  run "granule vm exec $VM_LOCAL -- cat /root/hello.txt"

  note "A VM that exists for exactly one command: created, waited for, used," \
       "deleted. This is the shape a CI job wants."
  run "granule vm run $VM_ONESHOT -kontur-image '$IMAGE' -- uname -a"

  note "Tear the long-lived one down. -shutdown-timeout is honoured: the" \
       "guest is asked to power off before anything is killed."
  run "granule vm delete $VM_LOCAL"
}

# ---------------------------------------------------------------------------
# Chapter 4: gke
#
# The same VM, the same exec, the same logs -- on a cluster, from the
# same published image, with no controller, CRD or runtime class
# installed. The only thing GKE has to supply is a real /dev/kvm.
# ---------------------------------------------------------------------------

chapter_gke() {
  chapter gke \
    "The same image, the same exec, the same logs -- on GKE." \
    "No controller, no CRD, no runtime class: one pod manifest."

  if ! have gcloud && skip_chapter "no gcloud on PATH"; then return 0; fi
  if [ -z "$GKE_PROJECT" ] && skip_chapter "GKE_PROJECT is unset"; then return 0; fi

  note "A node pool with nested virtualization is a first-class GKE flag:" \
       "no custom node image and no VMX licence juggling. COS_CONTAINERD" \
       "needs 1.28.4-gke.1083000 or newer; UBUNTU_CONTAINERD any version."
  note "Is there a cluster already? This one is allowed to fail: 'no such" \
       "cluster' is an answer, and the next line is what acts on it."
  run -f "gcloud container clusters describe '$GKE_CLUSTER' --zone '$GKE_ZONE' --project '$GKE_PROJECT' --format='value(name,currentNodeCount)'"
  note "Create it only if it is not there. Six minutes the first time," \
       "so this is the line to run before the audience arrives."
  run "gcloud container clusters describe '$GKE_CLUSTER' --zone '$GKE_ZONE' --project '$GKE_PROJECT' >/dev/null 2>&1 || \
  gcloud container clusters create '$GKE_CLUSTER' \
    --project '$GKE_PROJECT' --zone '$GKE_ZONE' \
    --num-nodes '$GKE_NODES' --machine-type '$GKE_MACHINE' \
    --image-type COS_CONTAINERD --enable-nested-virtualization"

  run "gcloud container clusters get-credentials '$GKE_CLUSTER' --zone '$GKE_ZONE' --project '$GKE_PROJECT'"
  run "kubectl get nodes -o wide"

  note "Proof the flag did what it says: /dev/kvm on the node, world" \
       "readable. Opening it needs no capability; creating a VM through it" \
       "is what privileged: true below is for."
  run "kubectl run kvm-check --rm -i --restart=Never --image=busybox --privileged --command -- ls -l /dev/kvm"

  note "The pod is the repo's own example with our published image" \
       "substituted in. Everything it needs -- the guest disk, the kernel," \
       "the runtime -- is inside that image already."
  run "sed 's|IMAGE_PLACEHOLDER|$IMAGE|' deploy/k8s/gke-pod-exec-example.yaml | kubectl apply -f -"

  note "The manifest's readiness probe is 'granule ready', so Ready means" \
       "the guest answered -- not merely that the container started."
  run "kubectl wait --for=condition=Ready pod/$POD_SINGLE --timeout=300s"
  run "kubectl get pod $POD_SINGLE -o wide"

  note "kubectl exec reaches the container; granule exec inside it reaches" \
       "the guest. Same vsock path as on docker, same command."
  run "kubectl exec $POD_SINGLE -- granule exec -- uname -a"
  run "kubectl exec $POD_SINGLE -- granule exec -- cat /etc/os-release"

  note "The guest holds the pod's own IP, so the cluster addresses it like" \
       "any other pod -- Services and all."
  run "kubectl exec $POD_SINGLE -- granule exec -- ip -brief addr"
  run "kubectl get pod $POD_SINGLE -o jsonpath='{.status.podIP}{\"\\n\"}'"

  note "And kubectl logs is the guest's serial console."
  run "kubectl logs --tail=25 $POD_SINGLE"

  run "kubectl delete pod $POD_SINGLE --wait=false"
}

# ---------------------------------------------------------------------------
# Chapter 5: pod
#
# Several VMs in one pod, one per container -- and then a new VM injected
# into that running pod as an ephemeral container, without restarting
# anything that was already in it.
# ---------------------------------------------------------------------------

chapter_pod() {
  chapter pod \
    "Several VMs in one pod, one per container -- then a new VM" \
    "injected into the running pod as an ephemeral container."

  if ! have kubectl && skip_chapter "no kubectl on PATH"; then return 0; fi
  if ! have_cluster && skip_chapter "kubectl has no cluster to talk to (run the gke chapter, or point KUBECONFIG at one)"; then
    return 0
  fi

  note "Three containers, three cloud-hypervisor VMs, one pod. One of them" \
       "(web) takes over the pod's IP with CHV_NET_MODE=inline; the other" \
       "two run CHV_NET_MODE=none, because a pod has one address and only" \
       "one guest can hold it. Exec still reaches all three: it goes over" \
       "each VM's own vsock, which needs no guest network at all."
  run "awk '/^    - name:/ {c=\$3} /^        - name: CHV_NET_MODE/ {getline; gsub(/\"/,\"\",\$2); printf \"  %-8s CHV_NET_MODE=%s\\n\", c, \$2}' '$assets/pod-multi-vm.yaml'"
  run "sed 's|IMAGE_PLACEHOLDER|$IMAGE|' '$assets/pod-multi-vm.yaml' | kubectl apply -f -"
  run "kubectl wait --for=condition=Ready pod/$POD_MULTI --timeout=300s"

  run "kubectl get pod $POD_MULTI -o jsonpath='{range .spec.containers[*]}{.name}{\"\\n\"}{end}'"

  note "Three separate machines, not three views of one. Each has its own" \
       "kernel boot id and its own uptime -- and its own memory: the pod's" \
       "limits are the sum of them."
  run "for c in web builder sidecar; do printf '%-8s ' \"\$c\"; kubectl exec $POD_MULTI -c \"\$c\" -- granule exec -- cat /proc/sys/kernel/random/boot_id; done"
  run "for c in web builder sidecar; do printf '%-8s ' \"\$c\"; kubectl exec $POD_MULTI -c \"\$c\" -- granule exec -- cut -d\" \" -f1 /proc/uptime; done"

  note "Only one of them holds the pod's address -- the pod has one, and" \
       "'web' took it over. The other two have no NIC at all, which is not" \
       "the same as being unreachable: every line above went over vsock."
  run "for c in web builder sidecar; do printf '%-8s ' \"\$c\"; kubectl exec $POD_MULTI -c \"\$c\" -- granule exec -- sh -c 'ip -brief addr show scope global || true' | tr '\\n' ' '; echo; done"
  run "kubectl get pod $POD_MULTI -o jsonpath='{.status.podIP}{\"\\n\"}'"

  note "Each container's console is that VM's console, addressed by -c."
  run "kubectl logs --tail=12 $POD_MULTI -c builder"

  note "Now the interesting part: a fourth VM into that pod, while it runs." \
       "An ephemeral container is the Kubernetes-native way to add a" \
       "container to a live pod -- normally a debugging shell; here it is a" \
       "whole machine. Nothing already in the pod restarts."
  run "cat '$assets/ephemeral-vm.yaml'"

  # `debug --custom` arrives in kubectl 1.31; 1.30 has --profile=sysadmin
  # but refuses --custom with "unknown flag". The three VMs above work on
  # any client, so this is checked here rather than at the top of the
  # chapter: a presenter on an older kubectl still gets the pod, and is
  # told in one line why the fourth VM is not coming. In --dry-run the
  # check never fires, because a rehearsal shows every line.
  if [ "$mode" = dry-run ] || kubectl debug --help 2>&1 | grep -q -- '--custom'; then
    note "--profile=sysadmin is what makes the injected container privileged," \
         "which is how it reaches /dev/kvm; --custom carries the env and the" \
         "volume mount an ephemeral container is allowed to have. (It may not" \
         "have resources, ports or probes: the API refuses those, which is" \
         "why this one declares none and why we wait on its status by hand.)"
    run "kubectl debug $POD_MULTI --image='$IMAGE' -c $EPHEMERAL_VM --profile=sysadmin --custom '$assets/ephemeral-vm.yaml'"

    note "That prints nothing when it works -- the pod is where the answer" \
         "is. The container is added to the running pod's spec, and the" \
         "kubelet starts it without touching anything already there."
    run "kubectl get pod $POD_MULTI -o jsonpath='{range .status.ephemeralContainerStatuses[*]}{.name}{\"\\t\"}{.state}{\"\\n\"}{end}'"

    note "Wait for it to be running before exec'ing into it: kubectl debug" \
         "returns as soon as the API accepts the container, and an exec into" \
         "one that is still ContainerCreating fails with 'container not" \
         "found'. There is no Ready condition to wait on -- an ephemeral" \
         "container may not have a probe -- so this waits on the field the" \
         "kubelet does set. It is a real pull: kubectl gives an injected" \
         ":latest image imagePullPolicy=Always."
    run "kubectl wait --for=jsonpath='{.status.ephemeralContainerStatuses[?(@.name==\"$EPHEMERAL_VM\")].state.running.startedAt}' pod/$POD_MULTI --timeout=300s"

    note "Then wait for the injected VM's guest the same way the readiness" \
         "probe would have, had an ephemeral container been allowed one."
    run "kubectl exec $POD_MULTI -c $EPHEMERAL_VM -- granule ready -timeout 180s"

    note "And it is a real, fourth machine: its own kernel, its own boot id."
    run "kubectl exec $POD_MULTI -c $EPHEMERAL_VM -- granule exec -- uname -a"
    run "for c in web builder sidecar $EPHEMERAL_VM; do printf '%-12s ' \"\$c\"; kubectl exec $POD_MULTI -c \"\$c\" -- granule exec -- cat /proc/sys/kernel/random/boot_id; done"
    run "kubectl logs --tail=12 $POD_MULTI -c $EPHEMERAL_VM"
  else
    printf '%s   ⊘ skipping the injected VM: this kubectl has no "debug --custom" (needs 1.31; 1.30 has --profile but not --custom)%s\n' \
      "$c_skip" "$c_off"
    skipped=$((skipped + 1))
  fi

  note "An ephemeral container cannot be removed or restarted -- that is" \
       "the API's rule, not granule's -- so the pod is how you take the" \
       "injected VM away again."
  run "kubectl delete pod $POD_MULTI --wait=false"
}

# ---------------------------------------------------------------------------
# Chapter 6: build
#
# The two ways to make a guest of your own, and the fact that they are
# the same operation reached by two names: pack an OCI image's filesystem
# into a bootable disk, or write a Dockerfile and let granule do the pack
# for you.
# ---------------------------------------------------------------------------

chapter_build() {
  chapter build \
    "Making a guest of your own: a Dockerfile that builds a VM, and" \
    "the pack of an ordinary OCI image underneath it."

  if ! have docker && skip_chapter "no docker on PATH"; then return 0; fi
  if ! have_image "$ROOTFS_IMAGE" &&
     skip_chapter "this daemon cannot get at $ROOTFS_IMAGE, which is the guest rootfs a bare build is based on -- run the publish chapter first, or set ROOTFS_IMAGE="; then
    return 0
  fi

  note "The Dockerfile is an ordinary one, with a smaller vocabulary. What" \
       "it describes is a machine: this one boots with nginx already" \
       "enabled as a systemd unit."
  run "cat '$assets/Dockerfile.guest'"

  note "What docker will actually be given: granule supplies the FROM (the" \
       "published guest rootfs) when the file names none. -print-dockerfile" \
       "validates and prints, and builds nothing."
  run "granule guest build -f '$assets/Dockerfile.guest' -t '$GUEST_IMAGE' -print-dockerfile"

  note "Instructions that cannot mean what they look like are refused by" \
       "name and line rather than dropped. A guest build runs on the build" \
       "host's kernel with no init, so there is no service to start and" \
       "nothing to run a container with -- that is what -setup is for."
  run -f "printf 'FROM $ROOTFS_IMAGE\\nADD https://example.com/x /x\\n' > '$DEMO_STATE/bad.Dockerfile' && granule guest build -f '$DEMO_STATE/bad.Dockerfile' -t nope:latest -print-dockerfile"

  note "And for real. Two steps in one command: an ordinary docker build" \
       "produces a rootfs image, and the packing turns that filesystem into" \
       "a bootable granule image -- granule's own guest half (the agents," \
       "the control-net overlay) laid over whatever the base carried."
  run -q "granule guest build -f '$assets/Dockerfile.guest' -t '$GUEST_IMAGE' -rootfs-tag '$GUEST_ROOTFS' -kontur-image '$VMM_IMAGE'"

  note "Two images came out. The rootfs is the one to publish: whoever" \
       "boots it packs it themselves, and gets that day's agents rather" \
       "than this build's."
  run "docker images --format 'table {{.Repository}}:{{.Tag}}\t{{.Size}}' | grep -E 'granule-demo|REPOSITORY'"

  note "The pack on its own, against that rootfs image. This is the verb" \
       "for an OCI image somebody else authored: it executes nothing out of" \
       "it and needs no KVM, so it can run on the machine holding the" \
       "daemon rather than in that consumer's CI."
  run "granule guest pack -from '$GUEST_ROOTFS' -t '$PACKED_IMAGE' -kontur-image '$VMM_IMAGE' -print-dockerfile"
  run -q "granule guest pack -from '$GUEST_ROOTFS' -t '$PACKED_IMAGE' -kontur-image '$VMM_IMAGE'"

  note "Boot what we built, and find the machine we described: nginx up," \
       "as a unit, started by the guest's own init. -ready-file is what" \
       "makes that a fair test -- without it 'ready' means the guest's agent" \
       "answered, which is a second or two before nginx has its socket, and" \
       "the curl below would race it. With it, ready means the workload's" \
       "own pidfile exists: the guest declaring itself up rather than" \
       "granule guessing on its behalf."
  run "granule vm run demo-built -kontur-image '$GUEST_IMAGE' -ready-file /run/nginx.pid -ready-timeout 180s -- sh -c 'systemctl is-enabled nginx; systemctl is-active nginx; curl -sS http://127.0.0.1:8080/whoami'"

  note "guest verify boots an image, runs named checks in it as root and" \
       "prints a verdict -- no commit, no tag, no image. -kontur-image" \
       "boots it under the VMM half a deployment will actually run it on," \
       "which is the machine worth judging."
  run "granule guest verify -image '$GUEST_IMAGE' -kontur-image '$VMM_IMAGE' -checks '$assets/checks.txt' -v"
}

# ---------------------------------------------------------------------------
# Chapter 7: rebase
#
# The payoff of the split: a guest somebody else customized months ago,
# moved onto today's runtime without rebuilding it -- in seconds, without
# opening the disk.
# ---------------------------------------------------------------------------

chapter_rebase() {
  chapter rebase \
    "Moving a guest somebody else built onto today's granule --" \
    "without rebuilding the guest, and without opening its disk."

  if ! have docker && skip_chapter "no docker on PATH"; then return 0; fi
  if ! docker image inspect "$GUEST_IMAGE" >/dev/null 2>&1 &&
     skip_chapter "there is no $GUEST_IMAGE to rebase -- run the build chapter first"; then
    return 0
  fi

  note "The premise: the image built in the last chapter is a consumer's." \
       "The runtime inside it is whatever granule they built against. A" \
       "cloud-hypervisor CVE lands; the operator upgrades; that guest has" \
       "to move -- and nobody wants to rebuild somebody else's machine."
  run "docker image inspect -f 'guest contract: {{index .Config.Labels \"dev.granule.guest.contract\"}}' '$GUEST_IMAGE'"

  note "The cheap form is the default, and it is the one to reach for: a" \
       "docker build whose FROM is the granule image, with the guest half" \
       "copied across. Seconds, no disk opened, nothing out of the guest" \
       "executed. Here is the build it runs."
  run "granule guest rebase -from '$GUEST_IMAGE' -t '$REBASED_IMAGE' -kontur-image '$VMM_IMAGE' -print-dockerfile"
  run "granule guest rebase -from '$GUEST_IMAGE' -t '$REBASED_IMAGE' -kontur-image '$VMM_IMAGE'"

  note "It is the same guest -- and the rebased image boots under the" \
       "runtime the operator controls."
  run "granule vm run demo-reb -kontur-image '$REBASED_IMAGE' -ready-file /run/nginx.pid -ready-timeout 180s -- sh -c 'systemctl is-active nginx; curl -sS http://127.0.0.1:8080/whoami; uname -r'"

  note "What the cheap form cannot refresh is the guest side: the agents" \
       "and the overlay inside the disk stay as they were packed. Where the" \
       "rootfs image still exists, -rootfs repacks it with today's agents --" \
       "minutes and a new disk, still running nothing out of the guest."
  run -q "granule guest rebase -from '$GUEST_IMAGE' -rootfs '$GUEST_ROOTFS' -t '$REBASED_FULL_IMAGE' -kontur-image '$VMM_IMAGE'"

  note "And where nobody has the rootfs any more, -boot is the last resort:" \
       "boot the guest under the operator's own runtime -- the cheap rebase" \
       "is built first, precisely so the half doing the booting is already" \
       "theirs -- and lay the current agents down from inside. It needs" \
       "/dev/kvm, and it is the only form that runs the guest's own code."
  note "  granule guest rebase -from IMAGE -boot -t NEW -kontur-image $VMM_IMAGE"

  note "The backstop under all three: the guest declares which contract" \
       "version it speaks (the label on step 01, and a copy beside the disk" \
       "that a boot reads before cloud-hypervisor starts). A guest below" \
       "granule's floor is refused rather than warned about, because the" \
       "cheap form is the one that leaves the disk alone -- so it is the" \
       "one that has to be able to say no. An image declaring nothing is" \
       "every image published before the declaration existed, so that is" \
       "reported instead of refused, and -force goes ahead regardless."
  note "Both halves fail closed, and the other half is worth seeing: a" \
       "-kontur-image that is not a granule image at all is refused before" \
       "anything is built, rather than producing an image nobody can boot."
  run -f "granule guest rebase -from '$GUEST_IMAGE' -t nope:latest -kontur-image debian:bookworm-slim"

  run "docker images --format 'table {{.Repository}}:{{.Tag}}\t{{.CreatedSince}}\t{{.Size}}' | grep -E 'granule-demo|REPOSITORY'"
}

# ---------------------------------------------------------------------------
# cleanup
# ---------------------------------------------------------------------------

chapter_cleanup() {
  chapter cleanup "Everything this demo made, taken away again."

  run -f "granule vm list"
  run -f "for vm in $VM_LOCAL $VM_ONESHOT demo-built demo-reb; do granule vm delete \"\$vm\" || true; done"
  run -f "docker rm -f \$(docker ps -aq --filter 'name=granule-vm-') 2>/dev/null || true"
  run -f "kubectl delete pod $POD_SINGLE $POD_MULTI --ignore-not-found --wait=false"
  run -f "docker rmi -f '$GUEST_IMAGE' '$GUEST_ROOTFS' '$PACKED_IMAGE' '$REBASED_IMAGE' '$REBASED_FULL_IMAGE' 2>/dev/null || true"
  run -f "rm -rf '$DEMO_STATE'"

  note "The GKE cluster is left alone on purpose -- it costs money and it" \
       "takes minutes to make. Delete it when you are done:"
  note "  gcloud container clusters delete $GKE_CLUSTER --zone $GKE_ZONE --project \${GKE_PROJECT:-PROJECT}"
}

# ---------------------------------------------------------------------------
# Driver
# ---------------------------------------------------------------------------

all_chapters="provision publish local gke pod build rebase"

usage() {
  cat <<EOF
usage: demo/demo.sh [options] [chapter ...]

Steps through granule's operations one line at a time: each step prints
the command, waits, runs it, and prints the result.

Chapters, in the order a full demo runs them:

  provision  prerequisites, this tree's CLI, and the images to build from
  publish    push granule's images to GHCR, and pull one back
  local      a VM from the published image on docker: create, exec, logs
  gke        the same VM, exec and logs on a GKE cluster
  pod        several VMs in one pod, one per container, plus a new VM
             injected into the running pod as an ephemeral container
  build      a guest from a Dockerfile, and the pack underneath it
  rebase     moving a guest onto today's granule without rebuilding it
  cleanup    remove what the demo made (not run by default)

With no chapter named, everything but cleanup runs, in that order.

Options:
  --dry-run       print every line of every chapter, run none of them.
                  Needs nothing installed; this is the rehearsal mode.
  --auto          don't wait for a keypress; pause --delay between steps
  --delay N       seconds to pause in --auto (default $delay)
  --keep-going    carry on after a step fails instead of stopping
  --no-color      no ANSI colour (also honours NO_COLOR)
  --list          list the chapters and exit
  -h, --help      this

Configuration is by environment variable -- REGISTRY, REPO, IMAGE_TAG,
IMAGE, LOCAL_IMAGE, RUNTIME_IMAGE, GKE_PROJECT, GKE_CLUSTER, GKE_ZONE,
GHCR_USER, GHCR_TOKEN, DEMO_STATE. See demo/README.md.
EOF
}

main() {
  local chapters=()
  while [ $# -gt 0 ]; do
    case $1 in
      --dry-run) mode=dry-run ;;
      --auto) mode=auto ;;
      --delay) delay=$2; shift ;;
      --keep-going) keep_going=true ;;
      --no-color) use_color=false ;;
      --list) printf '%s\n' $all_chapters cleanup; return 0 ;;
      -h|--help) usage; return 0 ;;
      -*) printf 'demo.sh: unknown option %s\n\n' "$1" >&2; usage >&2; return 2 ;;
      *) chapters+=("$1") ;;
    esac
    shift
  done
  set_colors

  if [ ${#chapters[@]} -eq 0 ]; then
    # shellcheck disable=SC2206 # deliberate word splitting of the list
    chapters=($all_chapters)
  fi

  # Validate the names before running anything: a typo in the fifth
  # chapter should not be found out forty minutes into a demo.
  local c
  for c in "${chapters[@]}"; do
    case " $all_chapters cleanup " in
      *" $c "*) ;;
      *) printf 'demo.sh: no such chapter %q (try --list)\n' "$c" >&2; return 2 ;;
    esac
  done

  cd "$repo_root"
  # A rehearsal makes nothing, not even the state directory: --dry-run is
  # run by CI in a checkout, and a mode whose whole claim is that it
  # changes nothing should not leave a directory behind.
  [ "$mode" = dry-run ] || mkdir -p "$DEMO_STATE"

  printf '%sgranule demo -- %s mode, chapters: %s%s\n' \
    "$c_head" "$mode" "${chapters[*]}" "$c_off"

  for c in "${chapters[@]}"; do
    step_no=0
    "chapter_$c"
  done

  printf '\n'
  rule
  if [ "$failures" -gt 0 ]; then
    printf '%sdone: %d step(s) failed, %d skipped%s\n' "$c_bad" "$failures" "$skipped" "$c_off"
    return 1
  fi
  printf '%sdone: every step succeeded (%d skipped)%s\n' "$c_ok" "$skipped" "$c_off"
}

main "$@"
