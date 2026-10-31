// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sandboximage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/bwsalmon/granule/pkg/guestimage"
)

// Contract is the version of the Grain-to-sandbox boundary this build of Grain
// produces and expects.
//
// The boundary is everything inside a sandbox image (Granule microVM guest or
// container/Kubernetes sandbox pod) that the Grain daemon and orchestrator rely
// on at runtime without installing it per run:
//   - user `debian` (uid 1000, home `/home/debian`, `/bin/bash`) and `/home/agent -> /home/debian`
//   - `git`, `curl`, and `ca-certificates` (`/etc/ssl/certs/ca-certificates.crt`)
//   - `python3` with `os, socket, sys, threading` for `pkg/capability/portforward.GuestForwarder`
//   - on Granule microVM backends: `systemd`, passwordless `sudo`, `/usr/local/bin/grain-connect`,
//     `/usr/lib/tmpfiles.d/grain.conf` (`d /run/grain 0755 root root -`), and Granule's own
//     guest contract (`dev.granule.guest.contract` / `/etc/granule/guest-contract`).
//   - `/usr/local/bin/grain` (cmd/grain-sandbox) with the sandbox CLI (`ls`, `read`, `grep`,
//     `ci`, `pr`, `task`, `api`) and `host-script`.
//
// Rules for changing these numbers:
//
//   - Bump Contract ONLY when Grain changes what it puts into the minimal base
//     sandbox images (`Granulefile.guest-base` or `Dockerfile.k8s-sandbox`'s
//     `k8s-sandbox-base` stage) or what the daemon/orchestrator requires from
//     inside the sandbox at runtime (for example: the `grain-connect` wire
//     protocol, `GuestForwarder` runtime requirements, required user/home
//     layout, or a bump in Granule's `dev.granule.guest.contract`).
//   - Do NOT bump Contract for changes to the daemon, UI, orchestrator logic
//     that does not change the in-sandbox boundary, or optional developer tools
//     in the full `guest` / `k8s-sandbox` images. Users maintaining custom
//     sandbox images `FROM ghcr.io/bwsalmon/grain/guest-base` or
//     `FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base` only need to rebuild
//     when Contract (or Granule's guest contract) changes, not on every Grain
//     commit.
//   - Bump MinimumContract ONLY when the daemon drops support for an older
//     boundary and can no longer drive an image built at that older contract.
//   - Every bump of either constant MUST add a numbered entry to the Contract
//     History below and in `docs/sandbox-image-contract.md`, and update the
//     stamps in `Granulefile.guest-base`, `Granulefile`, and
//     `Dockerfile.k8s-sandbox` (`TestContractIsDeclaredEverywhere` checks that
//     all three match `Contract`).
//
// Contract History:
//
//	1: Initial versioned Grain sandbox contract:
//	   - Base images: `ghcr.io/bwsalmon/grain/guest-base` (Granule microVM) and
//	     `ghcr.io/bwsalmon/grain/k8s-sandbox-base` (container/Kubernetes pod).
//	   - Stamps: OCI label `dev.grain.sandbox.contract="1"` and in-sandbox file
//	     `/etc/grain/sandbox-contract` containing `"1\n"`.
//	   - Required common boundary: user `debian` (uid 1000, `/home/debian`),
//	     symlink `/home/agent -> /home/debian`, `git`, `curl`, `ca-certificates`,
//	     `python3` (`import os, socket, sys, threading` for `GuestForwarder`).
//	   - Required Granule microVM boundary: `systemd`, passwordless `sudo` for
//	     `debian`, `/usr/local/bin/grain-connect` (`/run/grain` tmpfiles.d),
//	     and Granule guest contract `dev.granule.guest.contract` = 1.
//	2: In-sandbox grain CLI (docs/design/sandbox-first-agent-surface.md):
//	   - Required common boundary: executable `/usr/local/bin/grain`
//	     (cmd/grain-sandbox) carrying the sandbox CLI -- `grain ls`, `read`,
//	     `grep`, `ci`, `pr`, `task`, `api` -- beside `grain host-script`. The
//	     orchestrator no longer offers those as MCP tools and tells every
//	     agent to run them in its sandbox, so an image whose `grain` only
//	     knows `host-script` leaves an agent with no way to read CI, its pull
//	     request or its own deployment.
//	   - MinimumContract rises with it: a v1 image may carry that older
//	     `grain`, and this daemon can no longer drive a run in it.
const (
	Contract        = 2
	MinimumContract = 2
)

const (
	// ContractLabel is the OCI image label carrying the integer contract
	// version of a Grain sandbox image (`guest-base`, `guest`,
	// `k8s-sandbox-base`, `k8s-sandbox`, or a custom image derived from them).
	ContractLabel = "dev.grain.sandbox.contract"

	// ContractFile is the path inside the sandbox root filesystem where the
	// same integer contract version is written as a single line (`"1\n"`).
	ContractFile = "/etc/grain/sandbox-contract"

	// GranuleContract, GranuleMinimumContract, GranuleContractLabel, and
	// GranuleContractFile re-export Granule's own guest contract constants
	// (`third_party/granule/pkg/guestimage`) so callers checking a Granule VM
	// sandbox image can inspect both contracts from one package.
	GranuleContract        = guestimage.Contract
	GranuleMinimumContract = guestimage.MinimumContract
	GranuleContractLabel   = guestimage.ContractLabel
	GranuleContractFile    = guestimage.ContractFile

	// DefaultGuestBaseImage and DefaultK8sSandboxBaseImage are the published
	// minimal base images that custom sandbox images build FROM.
	DefaultGuestBaseImage      = "ghcr.io/bwsalmon/grain/guest-base:latest"
	DefaultK8sSandboxBaseImage = "ghcr.io/bwsalmon/grain/k8s-sandbox-base:latest"

	// BackendGranule, BackendK8s, and BackendContainer name the sandbox
	// backend families for VerifyScript and RequiredComponents.
	BackendGranule   = "granule"
	BackendK8s       = "k8s"
	BackendContainer = "container"
)

// ContractValue returns Contract formatted as a decimal string for OCI labels
// and /etc/grain/sandbox-contract.
func ContractValue() string {
	return strconv.Itoa(Contract)
}

// ContractState classifies a sandbox image's declared contract version against
// the range [MinimumContract, Contract] this build of Grain supports.
type ContractState int

const (
	// ContractUndeclared: the image carries no contract label/file (built
	// before contract stamping existed, or not built FROM a Grain base image).
	ContractUndeclared ContractState = iota
	// ContractUnreadable: the label/file is present but not a positive integer.
	ContractUnreadable
	// ContractTooOld: the image's contract is below MinimumContract; the base
	// image must be updated.
	ContractTooOld
	// ContractSupported: the image's contract is within [MinimumContract, Contract].
	ContractSupported
	// ContractAhead: the image's contract is newer than this Grain binary's
	// Contract; Grain itself needs to be upgraded to match the image.
	ContractAhead
)

// String returns a short machine-readable identifier for a ContractState.
func (s ContractState) String() string {
	switch s {
	case ContractUndeclared:
		return "undeclared"
	case ContractUnreadable:
		return "unreadable"
	case ContractTooOld:
		return "too-old"
	case ContractSupported:
		return "supported"
	case ContractAhead:
		return "ahead"
	default:
		return "unknown"
	}
}

// ReadContract parses a raw contract string (from the ContractLabel OCI label
// or ContractFile) and classifies it against [MinimumContract, Contract].
func ReadContract(declared string) (int, ContractState) {
	return readContractRange(declared, MinimumContract, Contract)
}

func readContractRange(declared string, minVer, curVer int) (int, ContractState) {
	s := strings.TrimSpace(declared)
	if s == "" {
		return 0, ContractUndeclared
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		return 0, ContractUnreadable
	}
	switch {
	case v < minVer:
		return v, ContractTooOld
	case v > curVer:
		return v, ContractAhead
	default:
		return v, ContractSupported
	}
}

// DescribeContract returns a single-line human-readable description of a
// declared contract string alongside its ContractState.
func DescribeContract(declared string) (string, ContractState) {
	v, state := ReadContract(declared)
	switch state {
	case ContractUndeclared:
		return fmt.Sprintf("unknown (no %s label; this grain expects %s)", ContractLabel, expectedRange(MinimumContract, Contract)), state
	case ContractUnreadable:
		return fmt.Sprintf("%q (not a version integer; this grain expects %s)", strings.TrimSpace(declared), expectedRange(MinimumContract, Contract)), state
	case ContractTooOld:
		return fmt.Sprintf("v%d (older than v%d, the oldest this grain supports -- rebuild FROM a current guest-base or k8s-sandbox-base)", v, MinimumContract), state
	case ContractAhead:
		return fmt.Sprintf("v%d (newer than v%d, the newest this grain knows -- upgrade grain to match)", v, Contract), state
	default:
		if v < Contract {
			return fmt.Sprintf("v%d (supported; current is v%d)", v, Contract), state
		}
		return fmt.Sprintf("v%d (current)", v), state
	}
}

func expectedRange(minVer, curVer int) string {
	if minVer == curVer {
		return fmt.Sprintf("v%d", curVer)
	}
	return fmt.Sprintf("v%d..v%d", minVer, curVer)
}

// ContractCheck is the structured verdict of CheckContract / CheckImageLabels.
type ContractCheck struct {
	ImageContract          int           `json:"imageContract"`
	MinimumContract        int           `json:"minimumContract"`
	CurrentContract        int           `json:"currentContract"`
	State                  ContractState `json:"state"`
	GranuleContract        int           `json:"granuleContract,omitempty"`
	GranuleMinimumContract int           `json:"granuleMinimumContract,omitempty"`
	GranuleCurrentContract int           `json:"granuleCurrentContract,omitempty"`
	GranuleState           ContractState `json:"granuleState,omitempty"`
	OK                     bool          `json:"ok"`
	NeedsBaseRebuild       bool          `json:"needsBaseRebuild"`
	Summary                string        `json:"summary"`
}

// CheckContract evaluates an image's Grain sandbox contract version (and, when
// granuleContract != 0, its Granule guest contract version) against this build's
// supported contract ranges.
//
// Pass granuleContract == 0 for container/Kubernetes pod sandbox images that do
// not boot a Granule microVM. For Granule VM images where the Granule contract
// must also be verified even if undeclared, use CheckContractForBackend or
// CheckImageLabels.
func CheckContract(imageContract, granuleContract int) ContractCheck {
	return checkContracts(imageContract, stateForInt(imageContract, MinimumContract, Contract), granuleContract, stateForInt(granuleContract, GranuleMinimumContract, GranuleContract), granuleContract != 0)
}

// CheckContractForBackend evaluates imageContract and granuleContract, requiring
// granuleContract when IsGranuleBackend(backend) is true.
func CheckContractForBackend(imageContract, granuleContract int, backend string) ContractCheck {
	isGranule := IsGranuleBackend(backend) || granuleContract != 0
	return checkContracts(imageContract, stateForInt(imageContract, MinimumContract, Contract), granuleContract, stateForInt(granuleContract, GranuleMinimumContract, GranuleContract), isGranule)
}

// CheckImageLabels inspects an OCI image's label map for ContractLabel (and
// GranuleContractLabel when IsGranuleBackend(backend) is true or the label is
// present on the image).
func CheckImageLabels(labels map[string]string, backend string) ContractCheck {
	var rawSandbox, rawGranule string
	if labels != nil {
		rawSandbox = labels[ContractLabel]
		rawGranule = labels[GranuleContractLabel]
	}
	imgVer, imgState := readContractRange(rawSandbox, MinimumContract, Contract)
	granVer, granState := readContractRange(rawGranule, GranuleMinimumContract, GranuleContract)
	isGranule := IsGranuleBackend(backend) || strings.TrimSpace(rawGranule) != ""
	return checkContracts(imgVer, imgState, granVer, granState, isGranule)
}

func stateForInt(v, minVer, curVer int) ContractState {
	switch {
	case v == 0:
		return ContractUndeclared
	case v < 0:
		return ContractUnreadable
	case v < minVer:
		return ContractTooOld
	case v > curVer:
		return ContractAhead
	default:
		return ContractSupported
	}
}

func checkContracts(imgVer int, imgState ContractState, granVer int, granState ContractState, checkGranule bool) ContractCheck {
	res := ContractCheck{
		ImageContract:   imgVer,
		MinimumContract: MinimumContract,
		CurrentContract: Contract,
		State:           imgState,
		OK:              imgState == ContractSupported,
	}
	if imgState == ContractUndeclared || imgState == ContractUnreadable || imgState == ContractTooOld {
		res.NeedsBaseRebuild = true
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("grain sandbox contract: %s", describeVersionState(imgVer, imgState, MinimumContract, Contract, ContractLabel)))

	if checkGranule {
		res.GranuleContract = granVer
		res.GranuleMinimumContract = GranuleMinimumContract
		res.GranuleCurrentContract = GranuleContract
		res.GranuleState = granState
		if granState != ContractSupported {
			res.OK = false
		}
		if granState == ContractUndeclared || granState == ContractUnreadable || granState == ContractTooOld {
			res.NeedsBaseRebuild = true
		}
		parts = append(parts, fmt.Sprintf("granule guest contract: %s", describeVersionState(granVer, granState, GranuleMinimumContract, GranuleContract, GranuleContractLabel)))
	}

	res.Summary = strings.Join(parts, "; ")
	return res
}

func describeVersionState(v int, state ContractState, minVer, curVer int, label string) string {
	switch state {
	case ContractUndeclared:
		return fmt.Sprintf("undeclared (missing %s; expected %s)", label, expectedRange(minVer, curVer))
	case ContractUnreadable:
		return fmt.Sprintf("unreadable (%s is not a positive integer; expected %s)", label, expectedRange(minVer, curVer))
	case ContractTooOld:
		return fmt.Sprintf("v%d (older than minimum supported v%d; rebuild FROM a current base image)", v, minVer)
	case ContractAhead:
		return fmt.Sprintf("v%d (newer than v%d known by this daemon; upgrade grain)", v, curVer)
	default:
		if v < curVer {
			return fmt.Sprintf("v%d (supported; current is v%d)", v, curVer)
		}
		return fmt.Sprintf("v%d (current)", v)
	}
}

// IsGranuleBackend reports whether backend names a Granule microVM sandbox
// backend (`granule`, `vm`, `linux-kvm`, `k8s-granule`, `kubernetes-granule`).
func IsGranuleBackend(backend string) bool {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "granule", "vm", "linux-kvm", "k8s-granule", "kubernetes-granule":
		return true
	default:
		return false
	}
}

// SandboxCLIPath is where every sandbox image carries its own grain
// (cmd/grain-sandbox), and SandboxCLICommands are the sandbox CLI
// commands the orchestrator tells agents to run through it
// (pkg/orchestrator's grainCLISection) -- what VerifyScript asks
// `grain help` to list, so an image carrying an older, host-script-only
// grain fails the check instead of passing it.
const SandboxCLIPath = "/usr/local/bin/grain"

var SandboxCLICommands = []string{"ls", "read", "grep", "ci", "pr", "task", "api"}

// RequiredComponents returns the human-readable list of in-sandbox boundary
// components required for the given backend (`granule` vs `k8s`/`container`).
func RequiredComponents(backend string) []string {
	items := []string{
		fmt.Sprintf("%s (%s in %s)", ContractFile, expectedRange(MinimumContract, Contract), ContractLabel),
		"user debian (uid 1000, home /home/debian, shell /bin/bash)",
		"symlink /home/agent -> /home/debian",
		"git",
		"curl",
		"ca-certificates (/etc/ssl/certs/ca-certificates.crt)",
		"python3 with standard library modules: os, socket, sys, threading (pkg/capability/portforward.GuestForwarder)",
		SandboxCLIPath + " (cmd/grain-sandbox: the sandbox CLI -- " + strings.Join(SandboxCLICommands, ", ") + " -- and host-script)",
	}
	if IsGranuleBackend(backend) {
		items = append(items,
			fmt.Sprintf("%s (%s in %s)", GranuleContractFile, expectedRange(GranuleMinimumContract, GranuleContract), GranuleContractLabel),
			"sudo (passwordless for user debian)",
			"systemd (/lib/systemd/systemd or /sbin/init)",
			"/usr/local/bin/grain-connect (and /usr/lib/tmpfiles.d/grain.conf creating /run/grain)",
		)
	}
	return items
}

// VerifyScript returns a self-contained POSIX `/bin/sh` script that checks a
// sandbox root filesystem (when run inside a container or microVM) against
// Grain's sandbox contract and every required boundary component for backend.
func VerifyScript(backend string) string {
	isGranule := "0"
	if IsGranuleBackend(backend) {
		isGranule = "1"
	}
	return fmt.Sprintf(`set -eu
fail() {
  echo "sandbox-contract verify failed: $*" >&2
  exit 1
}

if [ ! -r %q ]; then
  fail "missing %s (expected contract %s)"
fi
grain_contract="$(tr -d ' \t\r\n' < %q)"
case "$grain_contract" in
  ''|*[!0-9]*) fail "%s contains non-integer '$grain_contract'" ;;
esac
if [ "$grain_contract" -lt %d ]; then
  fail "%s version $grain_contract is older than minimum supported %d"
fi
if [ "$grain_contract" -gt %d ]; then
  fail "%s version $grain_contract is newer than supported %d"
fi

debian_uid="$(id -u debian 2>/dev/null || true)"
if [ "$debian_uid" != "1000" ]; then
  fail "user debian uid is '${debian_uid:-missing}', want 1000"
fi
if [ ! -d /home/debian ]; then
  fail "/home/debian directory is missing"
fi
if [ ! -e /home/agent ]; then
  fail "/home/agent symlink is missing"
fi
agent_real="$(cd /home/agent 2>/dev/null && pwd -P || true)"
debian_real="$(cd /home/debian 2>/dev/null && pwd -P || true)"
if [ "$agent_real" != "$debian_real" ]; then
  fail "/home/agent resolves to '$agent_real', want '$debian_real'"
fi

for cmd in git curl python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    fail "required command '$cmd' is not on PATH"
  fi
done

if [ ! -r /etc/ssl/certs/ca-certificates.crt ]; then
  fail "missing /etc/ssl/certs/ca-certificates.crt (ca-certificates)"
fi

if ! python3 -c 'import os, socket, sys, threading' >/dev/null 2>&1; then
  fail "python3 cannot import required GuestForwarder modules (os, socket, sys, threading)"
fi

if [ %q = "1" ]; then
  if [ ! -r %q ]; then
    fail "missing %s (expected granule guest contract %s)"
  fi
  granule_contract="$(tr -d ' \t\r\n' < %q)"
  case "$granule_contract" in
    ''|*[!0-9]*) fail "%s contains non-integer '$granule_contract'" ;;
  esac
  if [ "$granule_contract" -lt %d ]; then
    fail "%s version $granule_contract is older than minimum supported %d"
  fi
  if [ "$granule_contract" -gt %d ]; then
    fail "%s version $granule_contract is newer than supported %d"
  fi
  if ! command -v sudo >/dev/null 2>&1; then
    fail "required command 'sudo' is not on PATH"
  fi
  if [ ! -x /lib/systemd/systemd ] && [ ! -x /usr/lib/systemd/systemd ] && [ ! -x /sbin/init ]; then
    fail "systemd init binary is missing"
  fi
  if [ ! -x /usr/local/bin/grain-connect ]; then
    fail "missing executable /usr/local/bin/grain-connect"
  fi
fi

%s
echo "sandbox-contract verify ok: grain=v${grain_contract}$([ %q = "1" ] && echo " granule=v${granule_contract}" || true)"
`,
		ContractFile, ContractFile, expectedRange(MinimumContract, Contract),
		ContractFile, ContractFile,
		MinimumContract, ContractFile, MinimumContract,
		Contract, ContractFile, Contract,
		isGranule,
		GranuleContractFile, GranuleContractFile, expectedRange(GranuleMinimumContract, GranuleContract),
		GranuleContractFile, GranuleContractFile,
		GranuleMinimumContract, GranuleContractFile, GranuleMinimumContract,
		GranuleContract, GranuleContractFile, GranuleContract,
		sandboxCLIVerify(),
		isGranule,
	)
}

// sandboxCLIVerify is VerifyScript's check of SandboxCLIPath: that it is
// there, and that its own usage offers every SandboxCLICommands entry and
// host-script. GRAIN_SANDBOX_CLI=0 makes it print that usage even in a
// sandbox whose proxy credential would otherwise have it answer help over
// the proxy, so the verdict is the binary's and not the deployment's.
func sandboxCLIVerify() string {
	return fmt.Sprintf(`if [ ! -x %[1]q ]; then
  fail "missing executable %[1]s (cmd/grain-sandbox)"
fi
grain_help="$(GRAIN_SANDBOX_CLI=0 %[1]q help 2>&1 || true)"
for sub in %[2]s host-script; do
  if ! printf '%%s\n' "$grain_help" | grep -Eq "grain $sub( |\$)"; then
    fail "%[1]s does not offer 'grain $sub': it is an older grain without the sandbox CLI (rebuild FROM a current base image)"
  fi
done`, SandboxCLIPath, strings.Join(SandboxCLICommands, " "))
}

// BaseImageFor derives the corresponding minimal base image reference
// (`guest-base` or `k8s-sandbox-base`) carrying the same tag as a full sandbox
// image reference (strip-digesting `@sha256:...` because the base image has its
// own manifest digest).
func BaseImageFor(fullImage, baseName, fallback string) string {
	ref := strings.TrimSpace(fullImage)
	if ref == "" {
		return fallback
	}
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		ref = ref[:at]
	}
	tag := "latest"
	repo := ref
	if colon := strings.LastIndexByte(ref, ':'); colon > strings.LastIndexByte(ref, '/') {
		repo = ref[:colon]
		if t := strings.TrimSpace(ref[colon+1:]); t != "" {
			tag = t
		}
	}
	if slash := strings.LastIndexByte(repo, '/'); slash >= 0 {
		return repo[:slash+1] + baseName + ":" + tag
	}
	return fallback
}

// ContractInfo summarizes the current Grain sandbox contract, Granule guest
// contract, published base images, and required boundary components for
// `grain sandbox-image contract`.
type ContractInfo struct {
	Contract               int      `json:"contract"`
	MinimumContract        int      `json:"minimumContract"`
	ContractLabel          string   `json:"contractLabel"`
	ContractFile           string   `json:"contractFile"`
	GranuleContract        int      `json:"granuleContract"`
	GranuleMinimumContract int      `json:"granuleMinimumContract"`
	GranuleContractLabel   string   `json:"granuleContractLabel"`
	GranuleContractFile    string   `json:"granuleContractFile"`
	GuestBaseImage         string   `json:"guestBaseImage"`
	K8sSandboxBaseImage    string   `json:"k8sSandboxBaseImage"`
	DefaultGuestImage      string   `json:"defaultGuestImage,omitempty"`
	DefaultK8sSandboxImage string   `json:"defaultK8sSandboxImage,omitempty"`
	RequiredCommon         []string `json:"requiredCommon"`
	RequiredGranule        []string `json:"requiredGranule"`
}

// GetContractInfo returns the ContractInfo for this build of Grain, deriving
// the matching `guest-base` and `k8s-sandbox-base` tags from defaultGuestImage
// and defaultK8sSandboxImage when provided.
func GetContractInfo(defaultGuestImage, defaultK8sSandboxImage string) ContractInfo {
	common := RequiredComponents(BackendK8s)
	allGranule := RequiredComponents(BackendGranule)
	var granuleOnly []string
	if len(allGranule) > len(common) {
		granuleOnly = allGranule[len(common):]
	}
	return ContractInfo{
		Contract:               Contract,
		MinimumContract:        MinimumContract,
		ContractLabel:          ContractLabel,
		ContractFile:           ContractFile,
		GranuleContract:        GranuleContract,
		GranuleMinimumContract: GranuleMinimumContract,
		GranuleContractLabel:   GranuleContractLabel,
		GranuleContractFile:    GranuleContractFile,
		GuestBaseImage:         BaseImageFor(defaultGuestImage, "guest-base", DefaultGuestBaseImage),
		K8sSandboxBaseImage:    BaseImageFor(defaultK8sSandboxImage, "k8s-sandbox-base", DefaultK8sSandboxBaseImage),
		DefaultGuestImage:      defaultGuestImage,
		DefaultK8sSandboxImage: defaultK8sSandboxImage,
		RequiredCommon:         common,
		RequiredGranule:        granuleOnly,
	}
}

// FormatContractInfo renders ContractInfo as human-readable text for
// `grain sandbox-image contract`.
func FormatContractInfo(info ContractInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Grain sandbox contract:   v%d (minimum supported: v%d)\n", info.Contract, info.MinimumContract)
	fmt.Fprintf(&b, "  OCI label:              %s=%q\n", info.ContractLabel, strconv.Itoa(info.Contract))
	fmt.Fprintf(&b, "  In-sandbox file:        %s\n", info.ContractFile)
	fmt.Fprintf(&b, "Granule guest contract:   v%d (minimum supported: v%d)\n", info.GranuleContract, info.GranuleMinimumContract)
	fmt.Fprintf(&b, "  OCI label:              %s=%q\n", info.GranuleContractLabel, strconv.Itoa(info.GranuleContract))
	fmt.Fprintf(&b, "  In-sandbox file:        %s\n", info.GranuleContractFile)
	fmt.Fprintf(&b, "\nMinimal base images (rebuild custom images only when contract versions bump):\n")
	fmt.Fprintf(&b, "  Granule microVM base:   %s\n", info.GuestBaseImage)
	fmt.Fprintf(&b, "  Container / K8s base:   %s\n", info.K8sSandboxBaseImage)
	if info.DefaultGuestImage != "" || info.DefaultK8sSandboxImage != "" {
		fmt.Fprintf(&b, "\nDefault full sandbox images (stamped for this build):\n")
		if info.DefaultGuestImage != "" {
			fmt.Fprintf(&b, "  Granule microVM guest:  %s\n", info.DefaultGuestImage)
		}
		if info.DefaultK8sSandboxImage != "" {
			fmt.Fprintf(&b, "  Container / K8s pod:    %s\n", info.DefaultK8sSandboxImage)
		}
	}
	fmt.Fprintf(&b, "\nRequired boundary components (all sandbox backends):\n")
	for _, item := range info.RequiredCommon {
		fmt.Fprintf(&b, "  - %s\n", item)
	}
	if len(info.RequiredGranule) > 0 {
		fmt.Fprintf(&b, "\nAdditional required boundary components (Granule microVM backends):\n")
		for _, item := range info.RequiredGranule {
			fmt.Fprintf(&b, "  - %s\n", item)
		}
	}
	return b.String()
}

// ImageCheckReport is the result of `grain sandbox-image check <image>`.
type ImageCheckReport struct {
	Image       string            `json:"image"`
	Backend     string            `json:"backend"`
	Labels      map[string]string `json:"labels,omitempty"`
	Contract    ContractCheck     `json:"contract"`
	ProbeRan    bool              `json:"probeRan"`
	ProbeOK     bool              `json:"probeOk,omitempty"`
	ProbeOutput string            `json:"probeOutput,omitempty"`
	OK          bool              `json:"ok"`
}

// InspectAndCheckImage inspects an OCI image's labels via `docker image inspect`
// (and, when runProbe is true, runs VerifyScript inside a throwaway container)
// to verify compatibility with this build's sandbox contract.
func InspectAndCheckImage(ctx context.Context, dockerBin, image, backend string, runProbe bool) (ImageCheckReport, error) {
	if strings.TrimSpace(dockerBin) == "" {
		dockerBin = "docker"
	}
	image = strings.TrimSpace(image)
	if image == "" {
		return ImageCheckReport{}, fmt.Errorf("image reference is required")
	}

	inspectCmd := exec.CommandContext(ctx, dockerBin, "image", "inspect", "--format", "{{json .Config.Labels}}", image)
	inspectCmd.Env = os.Environ()
	out, err := inspectCmd.CombinedOutput()
	if err != nil {
		return ImageCheckReport{}, fmt.Errorf("inspecting image %s: %w (%s)", image, err, strings.TrimSpace(string(out)))
	}

	var labels map[string]string
	trimmed := strings.TrimSpace(string(out))
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal([]byte(trimmed), &labels); err != nil {
			return ImageCheckReport{}, fmt.Errorf("parsing labels for %s: %w", image, err)
		}
	}

	resolvedBackend := strings.ToLower(strings.TrimSpace(backend))
	if resolvedBackend == "" || resolvedBackend == "auto" {
		if (labels != nil && strings.TrimSpace(labels[GranuleContractLabel]) != "") || strings.Contains(image, "/guest") || strings.Contains(image, "guest-base") {
			resolvedBackend = BackendGranule
		} else {
			resolvedBackend = BackendK8s
		}
	}

	check := CheckImageLabels(labels, resolvedBackend)
	report := ImageCheckReport{
		Image:    image,
		Backend:  resolvedBackend,
		Labels:   labels,
		Contract: check,
		OK:       check.OK,
	}

	if runProbe {
		report.ProbeRan = true
		// A packed Granule VM image carries its guest filesystem as a disk
		// image under /usr/share/granule/rootfs.img rather than as the outer
		// container rootfs, so in-container `/bin/sh` probing is for container
		// and rootfs images; for packed Granule VM images we verify `/usr/share/granule/vmlinuz`
		// and `/usr/share/granule/rootfs.img` if `/etc/grain/sandbox-contract` is not in the outer layer.
		probeScript := VerifyScript(resolvedBackend)
		if IsGranuleBackend(resolvedBackend) {
			probeScript = fmt.Sprintf(`if [ -r %q ]; then
%s
elif [ -r /usr/share/granule/rootfs.img ] && [ -r /usr/share/granule/vmlinuz ]; then
  echo "sandbox-contract verify ok: packed granule VM image (/usr/share/granule/rootfs.img and vmlinuz present)"
else
  %s
fi`, ContractFile, VerifyScript(resolvedBackend), VerifyScript(resolvedBackend))
		}
		probeCmd := exec.CommandContext(ctx, dockerBin, "run", "--rm", "--entrypoint", "/bin/sh", image, "-c", probeScript)
		probeCmd.Env = os.Environ()
		probeOut, probeErr := probeCmd.CombinedOutput()
		report.ProbeOutput = strings.TrimSpace(string(probeOut))
		report.ProbeOK = probeErr == nil
		if !report.ProbeOK {
			report.OK = false
		}
	}

	return report, nil
}

// FormatImageCheckReport renders an ImageCheckReport as human-readable text.
func FormatImageCheckReport(r ImageCheckReport) string {
	var b strings.Builder
	status := "OK"
	if !r.OK {
		status = "FAILED"
	}
	fmt.Fprintf(&b, "%s: %s (backend: %s)\n", r.Image, status, r.Backend)
	fmt.Fprintf(&b, "  %s\n", r.Contract.Summary)
	if r.ProbeRan {
		probeStatus := "passed"
		if !r.ProbeOK {
			probeStatus = "FAILED"
		}
		if r.ProbeOutput != "" {
			fmt.Fprintf(&b, "  runtime probe: %s (%s)\n", probeStatus, r.ProbeOutput)
		} else {
			fmt.Fprintf(&b, "  runtime probe: %s\n", probeStatus)
		}
	}
	return b.String()
}
