// Package hostinit is `grain init`: bringing a bare Linux host up to a
// running deployment in one command, rather than the shell archaeology
// this package's own doc comment and commit history were written to
// retire. Every step below is something a real hand-run deployment
// needed and got wrong at least once before it was written down here:
//
//   - docker, Go (matching go.mod) and git, with docker specifically from
//     Docker's own apt repository -- Debian 12's bundled docker.io is
//     20.10, which granule does not run on -- and the Go tarball verified
//     against go.dev's own release index rather than a digest frozen into
//     this source tree, so a version bump here never ships a stale check.
//   - PlatformLinuxKVM only: the granule CLI, built from
//     third_party/granule through go.mod's own replace directive.
//     Cloning gke-labs/granule instead looks like the obvious move and
//     fails for a reason that reads like a permissions problem and is
//     not one: the module is vendored precisely so nothing here ever
//     needs to reach it.
//   - PlatformLinuxKVM only: the three images `granule granulefile
//     build` packs a guest onto, in the order they depend on each
//     other: the granule runtime image
//     (scripts/granule/build-granule-image.sh), then
//     Granulefile.guest-base, then Granulefile -- each build needing
//     -kontur-image to name the first, because the default reference
//     granulefile build writes is a Docker Hub name nothing here
//     publishes to. PlatformLinuxContainer builds none of this: its
//     sandboxes run the image the daemon's own -container-sandbox-image
//     default already names, pulled on first use like any other image.
//   - a docker network created before the daemon ever starts, so the
//     gateway `docker network inspect` reports is real rather than
//     guessed: a guessed git-proxy host address produces a first clone
//     that fails with "Couldn't connect to server" and nothing in that
//     message says why. Both platforms share this exact step and the
//     network it creates (ensureSandboxNetwork does not care which
//     backend ends up joining the network it reads a gateway off of);
//     they differ only in which daemon flag that gateway becomes
//     (-granule-git-proxy-host vs. -container-git-proxy-host).
//   - CAP_NET_BIND_SERVICE on the grain binary, since without it the
//     package cache cannot bind 443/80 and every sandbox falls back to
//     its own resolver with no transparent caching -- and this is a
//     property of the file, so it has to be re-applied after every
//     rebuild.
//   - the claude CLI, installed the way the deployment Dockerfile does:
//     fetched to a file, digest-checked, installed, and the installed
//     copy digest-checked again -- never `curl | bash`, whose exit status
//     is its last command's, so a 403 would "succeed" and leave a
//     deployment that fails at every dispatch.
//   - the frontend built before the grain binary, since pkg/ui/server.go
//     go:embeds it: a binary built first serves placeholder.html forever.
//   - the GitHub credential ladder, written from a token to
//     secrets/github/<name>.token plus a credentials.json with no "*"
//     entry, so a wide token is narrowed to the repos it is told about
//     rather than defaulting open.
//   - once the daemon is up, the things a healthy-looking deployment
//     still needs before an operator can actually use it, none of
//     which a store this package has no safe way to open directly
//     (the schema migrations it needs live in cmd/grain, not here) can
//     be set any other way than over that same daemon's own loopback
//     API, already a source it trusts as itself: the default team's
//     target repos (left at none, every task is parked rather than
//     dispatched, even one into a repo the credential ladder above was
//     just narrowed to), and the default team's GitHub credential (a
//     team clones with its own or with none, never the ladder's, since
//     grain/task-1592).
//
// Every step is get-or-create, the bar pkg/gcpsetup already holds itself
// to: running this twice is a no-op the second time, and a step that
// needs a privilege the caller does not have is recorded in Result.Steps
// as a manual step with the exact command an operator can run instead of
// aborting the whole attempt (see StepStatus). Several things are refused
// rather than guessed at, ever, with no manual fallback that papers over
// it: Options.Platform naming anything this package has no
// implementation for (preflight, below) -- a manual command printed for
// one on, say, darwin would be advice nothing could act on -- the
// git-proxy host address (ensureSandboxNetwork, shared by
// PlatformLinuxKVM and PlatformLinuxContainer alike), and starting a
// second daemon beside one already running (ensureDaemon) -- a
// suspended host resumes with its processes still live, so "my ssh
// session ended" is never evidence that the daemon stopped. Finding a
// daemon already running does not stop there, though: adoptRunningDaemon
// reads back its actual configuration -- an existing
// grain-daemon.service unit file if there already is one, or
// /proc/<pid>/cmdline and that process's own owner otherwise -- and
// writes a unit matching it exactly, so a daemon some earlier run (or an
// operator by hand) brought up with no systemd unit at all survives a
// reboot instead of this run silently imposing its own flag defaults on
// it. That adoption is itself refused, the same no-guessing way, the
// instant it cannot read the running daemon's configuration back at all,
// or the instant this run's own -data-dir or -platform disagrees with
// what is actually running. The two
// Kubernetes platforms each have their own refusals instead, in the
// same no-guessing spirit: PlatformK8sContainer refuses anything but a
// "baseline" Namespace pod-security level and a cluster whose CNI
// accepts NetworkPolicy objects without enforcing them
// (pkg/hostinit/k8s.go); PlatformK8sGranule refuses anything but
// "privileged" and a node pool that cannot actually boot a VM over
// /dev/kvm (pkg/hostinit/k8s_granule.go) -- opposite refusals, by
// design (docs/k8s-granule-design.md).
//
// Before any of the above runs at all, preflight checks every
// prerequisite Options.Platform depends on and reports every one that
// is missing together -- Init does nothing, installs nothing and
// builds nothing if any of them is absent, rather than discovering the
// first one only after the granule CLI and its three images have
// already been built. See Platform and preflight's own doc comments:
// today that list is goos==linux, make and node/npm for both host
// platforms, plus /dev/kvm for PlatformLinuxKVM alone; a cluster
// reachable by kubectl, RBAC, a pullable sandbox image and
// ReadWriteOnce storage for PlatformK8sContainer; and a cluster
// reachable by kubectl, RBAC, a "privileged" namespace and a real,
// scheduled nested-virt boot for PlatformK8sGranule.
//
// A test that calls Init itself, rather than initFor or ensureGoFor with
// an explicit goos/goarch, only runs on Linux: Init applies the
// PlatformLinuxKVM/PlatformLinuxContainer goos gate (preflight's
// checkGOOS) against runtime.GOOS before anything else, so on any other
// host it returns a single refused "host platform" step and nothing a
// test asserting real step behaviour can see. Use the seam instead and
// say "linux" explicitly, the way every such test here does; reaching
// Init directly is correct only for a test of the gate itself or one
// (like PlatformK8sContainer/PlatformK8sGranule) whose platform never
// checks goos at all.
package hostinit

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"time"
)

// StepStatus is what Init could do about one piece of the bootstrap --
// gcpsetup.StepStatus's own shape, with two additions this bootstrap
// needs that a GCP IAM call never does: StepSkipped, for a step that
// found its work already done, and StepRefused, for the handful of steps
// this package will not perform by guesswork no matter who runs it.
type StepStatus string

const (
	// StepDone means this step made the change, using the access Init
	// ran with. A step that finds its work already done without
	// changing anything reports StepSkipped instead, even one (like
	// ensureSandboxNetwork) that still has to read something to tell
	// the two cases apart.
	StepDone StepStatus = "done"
	// StepSkipped means there was nothing to do: an idempotency check
	// found the step's work already satisfied, or the step was never
	// asked for (e.g. no GitHub token given to write a credential
	// ladder with).
	StepSkipped StepStatus = "skipped"
	// StepManual means the access Init ran with was not enough -- most
	// commonly, not root for an apt install, a systemd unit or setcap.
	// Step.Command is the exact command an operator with more access
	// can run; re-running Init afterward finds it already done.
	StepManual StepStatus = "manual"
	// StepWould means -dry-run: Init would have performed this step,
	// described in Step.Detail/Step.Command, but changed nothing.
	StepWould StepStatus = "would"
	// StepRefused means Init will not do this step at all, on this run
	// or any other, because doing it would mean guessing at something
	// that has to be read off the real host instead -- seeing this
	// status is the signal that a human has to resolve the underlying
	// problem (see Step.Detail) rather than merely re-run with more
	// access.
	StepRefused StepStatus = "refused"
)

// Step is one unit of the bootstrap, in the order Init performs it.
type Step struct {
	Name    string
	Status  StepStatus
	Detail  string
	Command string // set for StepManual and most StepWould steps
}

// Result is everything Init did, in order -- a caller renders Result.Steps
// to walk an operator through exactly what happened and what is left.
type Result struct {
	Steps []Step

	// SandboxGitProxyHost is the gateway address ensureSandboxNetwork
	// read off the sandbox docker network, empty if that step was
	// refused or skipped (dry run) -- the value ensureDaemon passes as
	// -granule-git-proxy-host (PlatformLinuxKVM) or
	// -container-git-proxy-host (PlatformLinuxContainer).
	SandboxGitProxyHost string
	// GrainBinaryPath and GranuleBinaryPath are where the built binaries
	// ended up (or would, on a dry run), for a caller that wants to do
	// something with them beyond what Init itself did (starting the
	// daemon by hand, say).
	GrainBinaryPath   string
	GranuleBinaryPath string
}

// HasManual reports whether any step needs a human with more access than
// Init ran with before this deployment is actually up.
func (r Result) HasManual() bool {
	for _, s := range r.Steps {
		if s.Status == StepManual {
			return true
		}
	}
	return false
}

// HasRefused reports whether Init refused to perform some step rather
// than guess at it -- the signal that a human has to resolve something
// about this host before re-running gets any further, not merely grant
// more access.
func (r Result) HasRefused() bool {
	for _, s := range r.Steps {
		if s.Status == StepRefused {
			return true
		}
	}
	return false
}

func (r *Result) add(s Step) { r.Steps = append(r.Steps, s) }

// Options is one bootstrap run's configuration.
type Options struct {
	// RepoRoot is the grain checkout this is run from -- where go.mod,
	// the Granulefiles and scripts/granule live. Required.
	RepoRoot string
	// DataDir is the -data-dir a started daemon is given, and where the
	// GitHub credential ladder is written. Required.
	DataDir string
	// DryRun, if true, performs every read-only check Init normally
	// does (so the plan it prints reflects this host's real state) but
	// makes no change at all: every step that would otherwise act
	// becomes StepWould, describing the command it would have run.
	DryRun bool

	// Platform is the deployment shape this run targets -- see
	// Platform's own doc comment and docs/grain-init-platforms.md for
	// the full table. Empty means DefaultPlatform (PlatformLinuxKVM),
	// so a caller that has never heard of this field gets exactly the
	// behavior it always had. A value outside knownPlatforms is a bad
	// flag (validate rejects it); one of the four known platforms that
	// is not yet implemented is refused in preflight instead, naming
	// what implementing it would need.
	Platform Platform

	// SandboxNetworkName is the docker network created for sandboxes to
	// join -- the VM containers granule starts, on PlatformLinuxKVM, or
	// the sandbox containers themselves, on PlatformLinuxContainer --
	// so its gateway (-granule-git-proxy-host or
	// -container-git-proxy-host, depending on Platform) is read off a
	// network this host actually has rather than guessed at. Empty
	// means DefaultSandboxNetworkName.
	SandboxNetworkName string
	// GranuleRuntimeImage is the tag built by
	// scripts/granule/build-granule-image.sh and named with every
	// `granule granulefile build -kontur-image` this package runs.
	// Empty means DefaultGranuleRuntimeImage.
	GranuleRuntimeImage string
	// GuestBaseImage is the tag Granulefile.guest-base is built as.
	// Empty means DefaultGuestBaseImage.
	GuestBaseImage string
	// GuestImage is the tag Granulefile (the full guest) is built as,
	// and what the started daemon is pointed at with -sandbox-image.
	// Empty means DefaultGuestImage.
	GuestImage string

	// GrainBinaryPath is where `make build` is expected to leave the
	// grain binary, and what setcap and the started daemon both use.
	// Empty means <RepoRoot>/bin/grain, which is where the Makefile's
	// own $(CMDS) rule puts it.
	GrainBinaryPath string
	// UseBinary, if set, is a grain binary already on this host (a
	// release download, say) to run instead of building one: the build
	// step reports StepSkipped naming it, preflight demands neither make
	// nor node/npm, and on PlatformLinuxContainer -- which builds no
	// granule CLI either -- the Go toolchain step is skipped too. It
	// becomes GrainBinaryPath (setcap and the daemon's ExecStart both use
	// it), so setting GrainBinaryPath to anything else as well is
	// refused. Only the two Linux host platforms build a binary at all.
	UseBinary string
	// GranuleBinaryPath is where the granule CLI is built to. Empty
	// means <RepoRoot>/bin/granule.
	GranuleBinaryPath string

	// InstallClaudeCLI, if false, skips the claude CLI install step
	// entirely (StepSkipped) -- for a deployment that means to run only
	// the antigravity or codex frameworks. Defaults to true.
	InstallClaudeCLI *bool
	// ClaudeCLIVersion/ClaudeCLISHA256Amd64/ClaudeCLISHA256Arm64 pin the
	// claude CLI release, the same values the deployment Dockerfile
	// pins (CLAUDE_CLI_VERSION and friends) -- kept in step with it by
	// TestClaudeCLIDefaultsMatchDockerfile. Empty means those defaults.
	ClaudeCLIVersion     string
	ClaudeCLISHA256Amd64 string
	ClaudeCLISHA256Arm64 string
	// ClaudeHome is $HOME for the claude installer -- where it lays out
	// $HOME/.local the way claude.ai/install.sh would. Empty means
	// <DataDir>/claude.
	ClaudeHome string

	// GitHubCredentialName is the name the credential ladder's token is
	// written under (secrets/github/<name>.token). Required if
	// GitHubToken is set.
	GitHubCredentialName string
	// GitHubToken, if non-empty, is written as GitHubCredentialName's
	// credential and pointed at by every pattern in GitHubRepos. Empty
	// skips the whole credential-ladder step (StepSkipped): a deployment
	// with no token yet is expected to configure one later, through the
	// UI's Settings pane or `grain secrets github` directly.
	GitHubToken string
	// GitHubRepos is the set of "owner/repo" or "owner/*" patterns
	// GitHubToken is narrowed to. A literal "*" is refused
	// (StepRefused): the whole point of this ladder is that a wide
	// token is narrowed to named repos, and a "*" entry is exactly the
	// unnarrowed fallback it exists to avoid.
	GitHubRepos []string

	// GranuleSSHUser and GranuleWorkspace are the daemon's
	// -granule-ssh-user/-granule-workspace -- the guest account granule
	// authorizes its generated key for, and where it operates once
	// logged in as that account. Empty means DefaultGranuleSSHUser and
	// DefaultGranuleWorkspace, matching the Granulefile's own "debian"
	// guest account.
	GranuleSSHUser   string
	GranuleWorkspace string

	// StartDaemon, if false, stops after building and configuring
	// everything the daemon needs, without starting it -- for a caller
	// that wants to review the plan, or start the daemon itself with
	// its own extra flags. Defaults to true.
	StartDaemon *bool
	// ExtraDaemonArgs is appended, verbatim, to the daemon command line
	// Init writes into the systemd unit -- an escape hatch for anything
	// this package has no opinion of its own about (-agent-framework,
	// -max-workers, a model API key file, and so on).
	ExtraDaemonArgs []string
	// UpgradeAllowedBranches becomes the unit's -upgrade-allowed-branches:
	// the branches an upgrade may install, each a path.Match pattern or
	// upgrade.AnyBranchPattern. Empty keeps whatever the unit already on
	// disk passes (ensureDaemon), and with no such unit writes no flag,
	// leaving the daemon's own default (upgrade.DefaultAllowedBranches).
	UpgradeAllowedBranches []string
	// DaemonUser and DaemonGroup become the unit's own User=/Group= --
	// empty means DefaultDaemonUser (both; withDefaults sets DaemonGroup
	// to whatever DaemonUser ends up being, so a custom DaemonUser with
	// no DaemonGroup of its own gets a same-named group rather than
	// "grain"'s). Init get-or-creates this account (a system account,
	// no login shell, created once and reused across re-runs rather than
	// reusing whoever invoked grain init), get-or-adds it to the
	// "docker" group, and chowns DataDir (and ClaudeHome, if it lives
	// outside DataDir) to it before the daemon ever starts -- all three
	// idempotent, so re-running this against a deployment some earlier
	// version of this package already brought up as root migrates it
	// onto the new account rather than leaving it stuck. The one value
	// that opts all the way back out, with none of the above performed,
	// is the literal "root": that was every run's only behavior before
	// this field existed, and naming it explicitly keeps that choice
	// available rather than silently dropping it when the default
	// changed out from under an existing flag.
	DaemonUser  string
	DaemonGroup string

	// UIAddr is the host:port the daemon's UI/API listens on -- written
	// into a host platform's unit as -ui-addr unless ExtraDaemonArgs
	// passes one itself, in which case this has to be told to match.
	// Empty means DefaultUIAddr on a fresh install, and on a re-run the
	// -ui-addr the installed unit already passes (settleUIAddr), so an
	// existing deployment never moves on its own. withDefaults turns it
	// into the address the steps below dial: a wildcard host becomes
	// 127.0.0.1. ensureTeamTargetRepos and ensureTeamGitCredential
	// both reach the
	// daemon here, over loopback (already a trusted source --
	// pkg/ui/principal.go's isTrustedSource), rather than guess at a
	// store this package has no safe way to open directly: the schema
	// migrations a store needs live in cmd/grain, not here.
	UIAddr string
	// uiBindAddr is UIAddr as given, before withDefaults made it
	// dialable; empty when none was given.
	uiBindAddr string
	// uiDaemonArgs are the -ui-addr and -ui-allowed-hosts settleUIAddr
	// decided ensureDaemon writes into the unit.
	uiDaemonArgs []string
	// DaemonReadyTimeout and DaemonReadyPollInterval bound how long
	// Init waits, after starting (or finding already running) the
	// daemon, for that same API to answer before giving up on it and
	// reporting the steps after it as StepManual. Empty/zero means the
	// defaults below.
	DaemonReadyTimeout      time.Duration
	DaemonReadyPollInterval time.Duration

	// K8s configures -platform=k8s-container: see K8sOptions' own doc
	// comment (pkg/hostinit/k8s.go) for what each field names. Ignored
	// by every other Platform.
	K8s K8sOptions
	// K8sPortForward, for -platform=k8s-container only, establishes
	// something answering at UIAddr immediately before ProvisionOverAPI
	// runs and tears it down immediately after -- see k8s.go's own doc
	// comment for why this platform owns that much. withDefaults sets
	// this to k8sPortForwardViaKubectl when it is nil and
	// K8s.ManagePortForward is not false; a caller (chiefly a test)
	// that sets this field itself before withDefaults runs is never
	// overridden.
	K8sPortForward func(ctx context.Context, opts Options) (stop func(), err error)

	// K8sGranule configures -platform=k8s-granule: see
	// K8sGranuleOptions' own doc comment (pkg/hostinit/k8s_granule.go)
	// for what each field names. Ignored by every other Platform.
	K8sGranule K8sGranuleOptions
	// K8sGranulePortForward is K8sPortForward's own counterpart for
	// -platform=k8s-granule only.
	K8sGranulePortForward func(ctx context.Context, opts Options) (stop func(), err error)

	// GKESandboxes configures -platform=gke-sandboxes: see
	// GKESandboxesOptions' own doc comment (pkg/hostinit/gke_sandboxes.go)
	// for what each field names. Ignored by every other Platform.
	GKESandboxes GKESandboxesOptions
}

// Defaults an Init run falls back to absent an operator override --
// exported so a caller building its own Options literal, or a test
// asserting on them, has one place to read them from.
const (
	DefaultSandboxNetworkName  = "grain-sandboxes"
	DefaultGranuleRuntimeImage = "granule:vendored"
	DefaultGuestBaseImage      = "granule-guest-base:vendored"
	DefaultGuestImage          = "grain-guest:dev"
	DefaultGranuleSSHUser      = "debian"
	DefaultGranuleWorkspace    = "/home/debian"

	// DefaultDaemonUser is the unprivileged system account `grain init`
	// creates (if missing) and runs the daemon as, unless -daemon-user
	// says otherwise -- including the literal "root", which opts all
	// the way back out to systemd's own default and every behavior this
	// package had before this account existed.
	DefaultDaemonUser = "grain"

	// DefaultClaudeCLIVersion and the two digests below pin the same
	// release the deployment Dockerfile does -- kept in step with it by
	// TestClaudeCLIDefaultsMatchDockerfile in this package.
	DefaultClaudeCLIVersion     = "2.1.286"
	DefaultClaudeCLISHA256Amd64 = "fe503f65c6289d59c23e5b21ae44f03583f997dd33a2cbfc75ab4f96fb8fc73f"
	DefaultClaudeCLISHA256Arm64 = "0292fa22ac2fd43e16be9d0e511ddd8347280d6e0ebaca744ef5b27e05d8d0f8"

	// DefaultUIAddr matches the daemon's own -ui-addr default
	// (pkg/cli/daemon.go): every interface, on the port the grain CLI
	// defaults to. The provisioning steps still reach it over loopback
	// (withDefaults), a source the daemon already trusts as itself.
	DefaultUIAddr = "0.0.0.0:4242"
	// LegacyUIAddr is what the daemon listened on by default before
	// DefaultUIAddr -- and so what a unit an older grain init wrote,
	// which named no -ui-addr, was serving on. settleUIAddr pins it into
	// such a unit when it rewrites one rather than move the deployment.
	LegacyUIAddr = "127.0.0.1:8420"
	// DefaultDaemonReadyTimeout and DefaultDaemonReadyPollInterval
	// bound ensureDaemonReachable's poll of the daemon's own API after
	// starting it.
	DefaultDaemonReadyTimeout      = 30 * time.Second
	DefaultDaemonReadyPollInterval = 500 * time.Millisecond
)

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// withDefaults returns opts with every empty field replaced by its
// documented default -- never mutates the caller's copy.
func (o Options) withDefaults() Options {
	// Every path is made absolute first. docs/grain-init.md's own
	// example passes -repo-root ., and a relative path reached the
	// systemd unit as ExecStart=./bin/grain, which systemd rejects as a
	// bad unit file setting; it also made ensureGrainBinary see
	// "./bin/grain" and "bin/grain" as two files and copy the binary
	// onto itself ("text file busy").
	o.RepoRoot = absPath(o.RepoRoot)
	o.DataDir = absPath(o.DataDir)
	o.GrainBinaryPath = absPath(o.GrainBinaryPath)
	o.UseBinary = absPath(o.UseBinary)
	if o.GrainBinaryPath == "" {
		o.GrainBinaryPath = o.UseBinary
	}
	o.GranuleBinaryPath = absPath(o.GranuleBinaryPath)
	o.ClaudeHome = absPath(o.ClaudeHome)
	if o.Platform == "" {
		o.Platform = DefaultPlatform
	}
	if o.SandboxNetworkName == "" {
		o.SandboxNetworkName = DefaultSandboxNetworkName
	}
	if o.GranuleRuntimeImage == "" {
		o.GranuleRuntimeImage = DefaultGranuleRuntimeImage
	}
	if o.GuestBaseImage == "" {
		o.GuestBaseImage = DefaultGuestBaseImage
	}
	if o.GuestImage == "" {
		o.GuestImage = DefaultGuestImage
	}
	if o.GrainBinaryPath == "" {
		o.GrainBinaryPath = filepath.Join(o.RepoRoot, "bin", "grain")
	}
	if o.GranuleBinaryPath == "" {
		o.GranuleBinaryPath = filepath.Join(o.RepoRoot, "bin", "granule")
	}
	if o.ClaudeCLIVersion == "" {
		o.ClaudeCLIVersion = DefaultClaudeCLIVersion
	}
	if o.ClaudeCLISHA256Amd64 == "" {
		o.ClaudeCLISHA256Amd64 = DefaultClaudeCLISHA256Amd64
	}
	if o.ClaudeCLISHA256Arm64 == "" {
		o.ClaudeCLISHA256Arm64 = DefaultClaudeCLISHA256Arm64
	}
	if o.ClaudeHome == "" {
		o.ClaudeHome = filepath.Join(o.DataDir, "claude")
	}
	if o.GranuleSSHUser == "" {
		o.GranuleSSHUser = DefaultGranuleSSHUser
	}
	if o.GranuleWorkspace == "" {
		o.GranuleWorkspace = DefaultGranuleWorkspace
	}
	if o.DaemonUser == "" {
		o.DaemonUser = DefaultDaemonUser
	}
	if o.DaemonGroup == "" {
		o.DaemonGroup = o.DaemonUser
	}
	o.uiBindAddr = o.UIAddr
	if o.UIAddr == "" {
		o.UIAddr = DefaultUIAddr
	}
	o.UIAddr = loopbackDialAddr(o.UIAddr)
	if o.DaemonReadyTimeout == 0 {
		o.DaemonReadyTimeout = DefaultDaemonReadyTimeout
	}
	if o.DaemonReadyPollInterval == 0 {
		o.DaemonReadyPollInterval = DefaultDaemonReadyPollInterval
	}
	if o.K8s.Namespace == "" {
		o.K8s.Namespace = DefaultK8sNamespace
	}
	if o.K8s.Deployment == "" {
		o.K8s.Deployment = DefaultK8sDeployment
	}
	if o.K8s.Service == "" {
		o.K8s.Service = DefaultK8sService
	}
	if o.K8s.Manifest == "" {
		o.K8s.Manifest = o.RepoRoot + "/deploy/kubernetes/grain.yaml"
	}
	if o.K8s.NetworkPolicyManifest == "" {
		o.K8s.NetworkPolicyManifest = o.RepoRoot + "/deploy/kubernetes/networkpolicy.yaml"
	}
	if o.K8s.RolloutTimeout == 0 {
		o.K8s.RolloutTimeout = DefaultK8sRolloutTimeout
	}
	if o.K8s.PreflightTimeout == 0 {
		o.K8s.PreflightTimeout = DefaultK8sPreflightTimeout
	}
	if o.Platform == PlatformK8sContainer && o.K8sPortForward == nil && boolOr(o.K8s.ManagePortForward, true) {
		o.K8sPortForward = k8sPortForwardViaKubectl
	}
	if o.K8sGranule.Namespace == "" {
		o.K8sGranule.Namespace = DefaultK8sNamespace
	}
	if o.K8sGranule.Deployment == "" {
		o.K8sGranule.Deployment = DefaultK8sDeployment
	}
	if o.K8sGranule.Service == "" {
		o.K8sGranule.Service = DefaultK8sService
	}
	if o.K8sGranule.Manifest == "" {
		o.K8sGranule.Manifest = o.RepoRoot + "/deploy/kubernetes/grain-granule.yaml"
	}
	if o.K8sGranule.RolloutTimeout == 0 {
		o.K8sGranule.RolloutTimeout = DefaultK8sRolloutTimeout
	}
	if o.K8sGranule.PreflightTimeout == 0 {
		o.K8sGranule.PreflightTimeout = DefaultK8sGranulePreflightTimeout
	}
	if o.Platform == PlatformK8sGranule && o.K8sGranulePortForward == nil && boolOr(o.K8sGranule.ManagePortForward, true) {
		o.K8sGranulePortForward = k8sGranulePortForwardViaKubectl
	}
	o.GKESandboxes = o.GKESandboxes.withDefaults(o.RepoRoot)
	return o
}

// absPath is filepath.Abs for a path that is set, leaving "" alone (so
// validate still sees a missing RepoRoot or DataDir as missing rather
// than as the working directory) and leaving p as given in the
// vanishing case where the working directory cannot be read.
func absPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func (o Options) validate() error {
	if o.RepoRoot == "" {
		return fmt.Errorf("hostinit: RepoRoot is required")
	}
	if o.DataDir == "" {
		return fmt.Errorf("hostinit: DataDir is required")
	}
	if !o.Platform.known() {
		return fmt.Errorf("hostinit: %q is not a platform grain init has a name for (known: %v)", o.Platform, knownPlatforms())
	}
	if o.Platform == PlatformK8sContainer && o.K8s.SandboxImage == "" {
		return fmt.Errorf("hostinit: K8s.SandboxImage is required for -platform=%s -- there is no context-free "+
			"default this package could pick that would not go stale the moment CI publishes a new image", PlatformK8sContainer)
	}
	// Unlike K8s.SandboxImage above, K8sGranule.Image has nothing to
	// enforce here: cmd/grain's own -k8s-granule-image flag defaults it
	// to defaultSandboxImage, the same per-commit guest CI already
	// publishes and -platform=linux-kvm's -sandbox-image pulls, so a
	// caller going through that flag never reaches here with it empty.
	// This package still can't hardcode that default itself without it
	// going stale the moment CI republishes (the same reasoning
	// K8s.SandboxImage's own check is built on) -- so a caller that
	// bypasses the flag and leaves Image empty gets no refusal here,
	// only a pull failure at the preflight's own throwaway pod, which
	// is where an unpullable or missing image belongs.
	if o.UseBinary != "" {
		if o.Platform != PlatformLinuxKVM && o.Platform != PlatformLinuxContainer {
			return fmt.Errorf("hostinit: UseBinary (-use-binary) only applies to -platform=%s and -platform=%s, the two "+
				"platforms that build a grain binary on this host at all", PlatformLinuxKVM, PlatformLinuxContainer)
		}
		if o.GrainBinaryPath != o.UseBinary {
			return fmt.Errorf("hostinit: -use-binary %s and -grain-binary %s name two different binaries -- "+
				"-use-binary is the binary the daemon runs, so pass only that", o.UseBinary, o.GrainBinaryPath)
		}
	}
	if o.GitHubToken != "" && o.GitHubCredentialName == "" {
		return fmt.Errorf("hostinit: a GitHub token needs a name to be written under: pass -github-credential-name (GitHubCredentialName)")
	}
	for _, pattern := range o.GitHubRepos {
		if pattern == "*" {
			return fmt.Errorf(
				"hostinit: %q is not a usable entry in GitHubRepos: this ladder narrows a token to named repos on "+
					"purpose and refuses a \"*\" fallback that would undo it -- name the repos this token actually covers", "*")
		}
	}
	if err := checkUpgradeAllowedBranches(o.UpgradeAllowedBranches); err != nil {
		return fmt.Errorf("hostinit: %w", err)
	}
	return nil
}

// Init brings opts.RepoRoot's checkout up to a running deployment on
// this host, through sys. See this package's own doc comment for what
// each step does and why; every step below appends exactly the Step(s)
// its own helper decides on to the returned Result, in the order Init
// performs them, and a step after a failed prerequisite is marked
// StepSkipped rather than attempted against a prerequisite that is not
// there.
func Init(ctx context.Context, sys System, opts Options) (Result, error) {
	return initFor(ctx, sys, opts, runtime.GOOS)
}

// initFor is Init with the host platform taken explicitly rather than
// read from runtime.GOOS, so a test can exercise the refusal below
// without actually running on a non-Linux host -- the same seam
// ensureGoFor uses for goos/goarch.
func initFor(ctx context.Context, sys System, opts Options, goos string) (Result, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return Result{}, err
	}

	var r Result
	r.GrainBinaryPath = opts.GrainBinaryPath
	r.GranuleBinaryPath = opts.GranuleBinaryPath

	if !preflight(ctx, sys, &r, opts, goos) {
		return r, nil
	}

	if opts.Platform == PlatformK8sContainer {
		// None of the steps below apply: there is no local docker, Go
		// toolchain or granule CLI to build when the daemon runs as
		// somebody else's cluster's Deployment, built by CI and pulled
		// from a registry rather than by this process. See k8s.go's
		// own doc comment for this platform's entire post-preflight
		// path.
		initForK8sContainer(ctx, sys, opts, &r)
		return r, nil
	}

	if opts.Platform == PlatformK8sGranule {
		// None of the steps below apply, for the same reason they do
		// not under PlatformK8sContainer -- see k8s_granule.go's own
		// doc comment for this platform's entire post-preflight path.
		initForK8sGranule(ctx, sys, opts, &r)
		return r, nil
	}

	if opts.Platform == PlatformGKESandboxes {
		// Brings up or reuses an off-cluster GKE Standard sandbox
		// cluster and writes /var/lib/grain/kubeconfig -- see
		// gke_sandboxes.go.
		initForGKESandboxes(ctx, sys, opts, &r)
		return r, nil
	}

	ensureGit(ctx, sys, opts, &r)
	dockerOK := ensureDocker(ctx, sys, opts, &r)

	// Checked here, before any of the daemon-account steps below, not
	// only inside ensureDaemon's own canStart gate further down: a
	// daemon already running has whatever account, "docker" group
	// membership and data-dir ownership it needs already, under
	// whatever user actually started it -- which, on the kind of host
	// adopt mode exists for, is not opts.DaemonUser at all. Gating
	// adoption on readiness checks adoption itself has no need of is
	// what made it unreachable there; see this package's own doc
	// comment.
	adopting, adoptDesc := false, ""
	if boolOr(opts.StartDaemon, true) {
		adopting, adoptDesc, _ = daemonAlreadyRunning(ctx, sys)
	}

	// The account the daemon will run as (DefaultDaemonUser unless
	// -daemon-user says otherwise, including "root" to opt all the way
	// out) -- created, and added to "docker", as early as the host
	// allows rather than waiting for everything else to build first.
	accountOK := ensureDaemonAccount(ctx, sys, opts, &r, adopting, adoptDesc)
	ensureDaemonDockerGroup(ctx, sys, opts, &r, accountOK, dockerOK, adopting, adoptDesc)

	// Read before any image is built, not after: this is the same
	// refusal-over-guessing ensureSandboxNetwork always made, just no
	// longer sitting behind the most expensive step in the whole run --
	// a host this would refuse on learns that in milliseconds rather
	// than after three image builds.
	gateway := ""
	if dockerOK {
		gateway = ensureSandboxNetwork(ctx, sys, opts, &r)
	} else {
		r.add(Step{Name: "create the sandbox docker network", Status: StepSkipped, Detail: "skipped: docker is not ready yet"})
	}
	r.SandboxGitProxyHost = gateway

	// Only PlatformLinuxKVM boots a sandbox as a granule VM; the granule
	// CLI and the three images it packs a guest onto exist for that
	// platform alone. PlatformLinuxContainer's sandboxes are plain
	// docker containers running the image the daemon's own
	// -container-sandbox-image default already names, so there is
	// nothing here for it to build -- these two steps are skipped
	// outright rather than attempted and found unnecessary.
	needsGranule := opts.Platform == PlatformLinuxKVM

	// With -use-binary there is no grain binary to build either, so on a
	// platform that builds no granule CLI nothing in this run needs Go.
	var goOK bool
	var goEnv []string
	if opts.UseBinary != "" && !needsGranule {
		r.add(Step{Name: "go toolchain", Status: StepSkipped,
			Detail: fmt.Sprintf("skipped: -use-binary needs no grain build and -platform=%s builds no granule CLI", opts.Platform)})
	} else {
		goOK, goEnv = ensureGo(ctx, sys, opts, &r)
	}

	granuleOK := false
	switch {
	case !needsGranule:
		r.add(Step{Name: "build the granule CLI", Status: StepSkipped,
			Detail: fmt.Sprintf("skipped: -platform=%s runs sandboxes as plain docker containers and never calls granule", opts.Platform)})
	case goOK:
		granuleOK = ensureGranuleCLI(ctx, sys, opts, &r, goEnv)
	default:
		r.add(Step{Name: "build the granule CLI", Status: StepSkipped, Detail: "skipped: the Go toolchain step above did not succeed"})
	}

	imagesOK := false
	switch {
	case !needsGranule:
		r.add(Step{Name: "build the granule runtime, guest-base and guest images", Status: StepSkipped,
			Detail: fmt.Sprintf("skipped: -platform=%s pulls its sandbox image instead of building one", opts.Platform)})
	case dockerOK && granuleOK:
		imagesOK = ensureImages(ctx, sys, opts, &r)
	default:
		r.add(Step{Name: "build the granule runtime, guest-base and guest images", Status: StepSkipped,
			Detail: "skipped: docker or the granule CLI is not ready yet"})
	}

	grainBuiltOK := false
	if goOK || opts.UseBinary != "" {
		grainBuiltOK = ensureGrainBinary(ctx, sys, opts, &r, goEnv)
	} else {
		r.add(Step{Name: "build grain (frontend + binary)", Status: StepSkipped, Detail: "skipped: the Go toolchain step above did not succeed"})
	}
	if grainBuiltOK {
		ensureSetcap(ctx, sys, opts, &r)
	} else {
		r.add(Step{Name: "grant the grain binary CAP_NET_BIND_SERVICE", Status: StepSkipped, Detail: "skipped: the grain binary was not built"})
	}

	if boolOr(opts.InstallClaudeCLI, true) {
		ensureClaudeCLI(ctx, sys, opts, &r)
	} else {
		r.add(Step{Name: "install the claude CLI", Status: StepSkipped, Detail: "skipped: -install-claude-cli=false"})
	}

	ensureCredentialLadder(ctx, sys, opts, &r)

	// Chowned here, not earlier: ensureClaudeCLI and ensureCredentialLadder
	// above are the last two steps that write into DataDir/ClaudeHome as
	// whoever invoked grain init (root, for every step that needs it) --
	// everything the daemon itself creates from here on, it creates as
	// its own account.
	ownershipOK := ensureDataDirOwnership(ctx, sys, opts, &r, accountOK, adopting, adoptDesc)

	daemonStartedOrRunning := false
	var adopted *adoptedDaemon
	cliEnvDue := false
	if boolOr(opts.StartDaemon, true) {
		daemonUserReady := wantsRootDaemon(opts) || (accountOK && ownershipOK)
		canStart := grainBuiltOK && gateway != "" && (!needsGranule || imagesOK) && daemonUserReady
		switch {
		case adopting || canStart:
			opts = settleUIAddr(opts, &r)
			daemonStartedOrRunning, adopted = ensureDaemon(ctx, sys, opts, &r, gateway)
			cliEnvDue = true
		case !daemonUserReady:
			r.add(Step{Name: "start the daemon", Status: StepSkipped,
				Detail: "skipped: the account the daemon would run as is not ready yet -- see the steps above for creating it and giving it ownership of -data-dir"})
		default:
			r.add(Step{Name: "start the daemon", Status: StepSkipped,
				Detail: "skipped: an earlier step did not finish -- the binary, the images or the sandbox network gateway is missing"})
		}
	} else {
		r.add(Step{Name: "start the daemon", Status: StepSkipped, Detail: "skipped: -start-daemon=false"})
	}

	// Point the provisioning steps below at the daemon actually
	// adopted, not this run's own flag defaults -- those were never
	// asked about a process grain init did not start. -ui-addr only
	// changes if the adopted daemon's own argv actually names one
	// (absent, it is already running on the same default this run's
	// own opts.UIAddr would otherwise carry).
	if adopted != nil {
		if v, ok := argvFlagValue(adopted.Args, "ui-addr"); ok {
			opts.UIAddr = v
		}
	}

	if cliEnvDue {
		ensureCLIEnv(sys, opts, &r)
	}

	daemonUp := ensureDaemonReachable(ctx, sys, opts, &r, daemonStartedOrRunning)
	provisionReachableDaemon(ctx, sys, opts, &r, daemonUp)

	return r, nil
}
