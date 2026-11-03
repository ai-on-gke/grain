#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: test-image.sh IMAGE ubuntu-22.04|ubuntu-24.04|ubuntu-26.04|ubuntu-slim" >&2
  exit 2
fi

image=$1
label=$2
case "$label" in
  ubuntu-22.04) expected_version=22.04 ;;
  ubuntu-24.04|ubuntu-slim) expected_version=24.04 ;;
  ubuntu-26.04) expected_version=26.04 ;;
  *) echo "unsupported GitHub runner image: $label" >&2; exit 2 ;;
esac

for command in docker timeout; do
  command -v "$command" >/dev/null || {
    echo "missing required test command: $command" >&2
    exit 1
  }
done

container="granule-github-${label//./-}-$$"
jit_container="$container-jit"
cleanup() {
  status=$?
  if [[ $status -ne 0 ]]; then
    echo "GitHub runner image smoke test failed; recent runtime logs follow" >&2
    docker logs --tail 120 "$container" >&2 || true
    if docker inspect "$jit_container" >/dev/null 2>&1; then
      echo "and of the just-in-time runner container:" >&2
      docker logs --tail 120 "$jit_container" >&2 || true
    fi
  fi
  docker rm -f "$container" "$jit_container" >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

guest() {
  timeout 180s docker exec "$container" granule exec -- "$@"
}

wait_for_boot() {
  docker exec "$container" granule ready -timeout 180s
  boot_state=$(guest systemctl is-system-running --wait || true)
  case "$boot_state" in
    running|degraded) ;;
    *) echo "guest boot did not finish: $boot_state" >&2; return 1 ;;
  esac
}

docker run -d --name "$container" --privileged "$image" >/dev/null
wait_for_boot

actual_version=$(guest sh -c '. /etc/os-release; printf %s "$VERSION_ID"')
[[ $actual_version == "$expected_version" ]] || {
  echo "expected Ubuntu $expected_version, found $actual_version" >&2
  exit 1
}
[[ $(guest uname -m) == x86_64 ]] || {
  echo "guest is not x86-64" >&2
  exit 1
}

guest id runner >/dev/null
guest test -x /usr/local/bin/granule-agent
guest systemctl is-active --quiet granule-agent.service
guest systemctl is-active --quiet granule-control-net.service
guest sh -c 'test "$(findmnt -n -o SOURCE /)" = /dev/vda'
guest getent hosts github.com >/dev/null

# Keep this list to stable, cross-image entry points. It is deliberately
# broader than the conversion dependencies so a successfully booted but
# accidentally stripped GitHub environment cannot be published.
guest bash -lc '
  missing=0
  for tool in git git-lfs curl python3 node npm docker gh jq yq pwsh az aws gcloud; do
    if ! command -v "$tool" >/dev/null; then
      echo "missing GitHub runner tool: $tool" >&2
      missing=1
    fi
  done
  exit "$missing"
'

# The Actions runner is the guest's workload. Started without a
# just-in-time configuration, as here, it waits rather than exits, so the
# VM stays up; its binary has to start as the runner user for the image
# to be any use under Actions Runner Controller.
guest test -x /home/runner/actions-runner/run.sh
guest test -f /etc/granule/container.json
guest systemctl is-active --quiet granule-container.service
guest runuser -u runner -- /home/runner/actions-runner/bin/Runner.Listener --version >/dev/null

# A second boot catches stale machine state and one-shot conversion failures
# that a single successful startup would miss.
docker restart -t 60 "$container" >/dev/null
wait_for_boot
guest id runner >/dev/null
guest systemctl is-active --quiet granule-agent.service
docker rm -f "$container" >/dev/null

# The contract with Actions Runner Controller, end to end short of
# GitHub: ACTIONS_RUNNER_INPUT_JITCONFIG on the VM's container reaches the
# runner in the guest, and the container ends once the runner does. A
# placeholder configuration is one the runner rejects at once -- it never
# contacts GitHub -- and run.sh exits 0 after a rejected registration as
# it does after a finished job, so the VM powers off and the container
# exits 0, which is what ends an ARC runner pod.
docker run -d --name "$jit_container" --privileged \
  -e ACTIONS_RUNNER_INPUT_JITCONFIG=granule-test-image-placeholder "$image" >/dev/null
jit_status=$(timeout 300s docker wait "$jit_container") || {
  echo "the VM did not stop after its runner exited" >&2
  exit 1
}
jit_logs=$(docker logs "$jit_container" 2>&1)
grep -q 'granule-actions-runner: starting the Actions runner' <<<"$jit_logs" || {
  echo "the guest never started the Actions runner with ACTIONS_RUNNER_INPUT_JITCONFIG set" >&2
  exit 1
}
grep -q 'Exiting runner' <<<"$jit_logs" || {
  echo "the Actions runner's run.sh did not run to its end" >&2
  exit 1
}
[[ $jit_status == 0 ]] || {
  echo "the runner container exited $jit_status, want the runner's own 0" >&2
  exit 1
}

echo "validated $image as GitHub $label"
