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

// init.go is `grain init`: taking a bare Linux host to a running
// deployment in one command, so that bringing one up is a command rather
// than a session of shell archaeology. It wraps pkg/hostinit.Init the
// same way setup.go wraps pkg/gcpsetup.EnsureInfrastructure -- see that
// package's own doc comment for what each step does and why, and why a
// step this process lacks the privilege for is reported with the exact
// command rather than aborting the run.
//
// Unlike `grain setup gcp`, this runs directly against the host it is
// invoked on: there is no credential to authenticate with, only the
// access the invoking account already has (root or not). -dry-run prints
// the plan -- every read-only check Init performs, with every step that
// would otherwise act replaced by a description of what it would have
// done -- without changing anything.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/hostinit"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// absDataDir is dataDir made absolute, as hostinit does with it, so the
// token path init names is the one the daemon it starts writes to.
func absDataDir(dataDir string) string {
	if abs, err := filepath.Abs(dataDir); err == nil {
		return abs
	}
	return dataDir
}

// githubTokenShape matches an argument that looks like a live GitHub
// credential regardless of which flag it was handed to: gh[pousr]_ and
// github_pat_ are GitHub's own prefixes for personal, OAuth, user,
// server, and refresh tokens and the fine-grained PAT shape, and a
// 40-character hex string is the classic PAT shape GitHub issued before
// those prefixes existed. An argument this shape on a command line is
// visible to every other user on the host via `ps` for as long as this
// process runs, and a second copy survives in shell history -- a mistake
// worth refusing to start over, not working around.
var githubTokenShape = regexp.MustCompile(`^(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|[0-9a-fA-F]{40})$`)

func refuseTokenShapedArgs(args []string) error {
	for _, a := range args {
		if githubTokenShape.MatchString(a) {
			return fmt.Errorf("refusing to start: the argument %q looks like a live GitHub token -- "+
				"a token belongs in a file passed to -github-token-file, never as a bare command-line "+
				"argument, since `ps` would show it to every other user on this host for as long as "+
				"this process runs", a)
		}
	}
	return nil
}

// resolveGitHubToken reads the token -github-token-file names, preferring
// it over -github-token's literal value whenever both are given -- so a
// script that is updated to pass the safe flag is not silently overridden
// by an old -github-token it forgot to remove. -github-token is kept only
// for a transitional period: giving it prints the warn argument's reader
// the ps/shell-history exposure it carries.
func resolveGitHubToken(token, tokenFile string, warn io.Writer) (string, error) {
	resolved := token
	if token != "" {
		fmt.Fprintln(warn, "grain init: -github-token puts a live credential on the command line -- "+
			"`ps` shows it to every other user on this host for as long as this process runs, and "+
			"shell history keeps a second copy. Use -github-token-file instead.")
	}
	if tokenFile != "" {
		t, err := daemon.ReadTokenFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("-github-token-file: %w", err)
		}
		resolved = t
	}
	return resolved, nil
}

// initExitStepsRemain is what `grain init` exits with when it ran to
// completion but the deployment is not actually up and provisioned yet
// -- some step needed more access than this ran with (StepManual) or
// refused to guess at something that has to be resolved by hand
// (StepRefused). Distinct from both 0 (the deployment is up) and 1 (a
// genuine failure -- bad flags, or Init itself erroring) so a caller
// scripting against this command can tell "re-run after doing these
// things" apart from either.
const initExitStepsRemain = 10

func initCmd(args []string) {
	if len(args) > 0 && args[0] == "workstation" {
		initWorkstationCmd(args[1:])
		return
	}
	result, err := cmdInit(context.Background(), args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "grain init: "+err.Error())
	}
	os.Exit(initExitCode(result, err))
}

// initExitCode is the status `grain init` exits with after cmdInit
// returned (result, err): 1 for a genuine failure (err is non-nil --
// bad flags, or Init itself erroring), initExitStepsRemain once Init ran
// to completion but left a manual or refused step behind, and 0 only
// once the deployment is actually up and provisioned -- every step
// either done or deliberately skipped.
func initExitCode(result hostinit.Result, err error) int {
	if err != nil {
		return 1
	}
	if result.HasManual() || result.HasRefused() {
		return initExitStepsRemain
	}
	return 0
}

func cmdInit(ctx context.Context, args []string) (hostinit.Result, error) {
	if err := refuseTokenShapedArgs(args); err != nil {
		return hostinit.Result{}, err
	}

	fs := flag.NewFlagSet("grain init", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: grain init [flags]

-platform picks which of four deployment shapes this run targets (see
docs/grain-init-platforms.md); all four are implemented. What follows
describes linux-kvm (the default); linux-container shares every step
except that it never touches the granule CLI or the three images a
granule guest packs onto (nothing here to build -- it pulls its
sandbox image instead), and runs its sandboxes as plain docker
containers rather than granule VMs, so it comes up in well under a
minute rather than linux-kvm's ten-plus. The two Kubernetes platforms
share almost none of it: each one's own preflight reads a cluster
instead of a host (see the -k8s-* flags, docs/k8s-init-design.md and
docs/k8s-granule-design.md), and each path after preflight is "kubectl
apply" of its own manifest exactly once (deploy/kubernetes/grain.yaml
for k8s-container, deploy/kubernetes/grain-granule.yaml for
k8s-granule), a wait for the rollout, and the same provisioning
described below over a "kubectl port-forward" this process starts and
stops around it. The two Kubernetes platforms' own preflights refuse
opposite things about the same cluster property (whether a sandbox pod
may be privileged), which is why most -k8s-* flags below are shared
between them (the cluster, the Deployment/Service names, the node
selector) while -k8s-sandbox-image and -k8s-granule-image name two
different kinds of image for the two platforms' very different
sandbox pods.

Takes this host from a bare Linux install to a running grain deployment:
docker (from Docker's own apt repository), Go (matching go.mod, verified
against go.dev's own release index), git, the granule CLI (built from
third_party/granule), the three images a granule guest packs onto (in the
order they depend on each other), a docker network for the sandbox VMs to
join (so -granule-git-proxy-host is read off a real gateway, never
guessed), the grain binary itself (frontend built first),
CAP_NET_BIND_SERVICE on it, the claude CLI (digest-verified before and
after install), and -- given a token -- a GitHub credential ladder with no
"*" fallback. It also get-or-creates the unprivileged account the daemon
runs as (-daemon-user, `+"`"+hostinit.DefaultDaemonUser+"`"+` unless told
otherwise), adds it to the "docker" group, and chowns -data-dir to it --
the daemon runs as that account rather than root; name -daemon-user=root
explicitly to opt back into root. Then, unless -start-daemon=false, it
starts the daemon, waits for its API to answer,
and -- over that same loopback API, already a source the daemon trusts as
itself -- points the default team's target repos at
-github-repo (leaving a credential ladder narrowed to repos the team
cannot dispatch into is its own kind of trap), and points the default team at
-github-credential-name's token if it has no credential of its own (a team
no longer falls back to the deployment's ladder).

Every step is get-or-create: running this again is a no-op wherever the
work is already done, and picks up from a manual step (see its own
output) once an operator has run the command it named. It never starts
a second daemon beside one already running -- found by systemctl
is-active against grain-daemon.service, or a pgrep match for "grain
daemon" -- the same whether grain init itself started that daemon on
an earlier run or an
operator brought it up some other way entirely. Instead it adopts:
reading back that daemon's actual configuration (an existing
grain-daemon.service unit file if there already is one, or
/proc/<pid>/cmdline and the process's own owner otherwise) and writing
a unit that matches it exactly, rather than one built from this
command's own flags -- so a daemon brought up by hand, with no unit at
all, survives a reboot without this command silently imposing a
different -data-dir, -ui-addr or sandbox shape onto it. The one thing
it still refuses outright: guessing at the sandbox network's gateway
(or, for k8s-container, applying over a deployment that already
exists), and adopting a daemon whose actual -data-dir or sandbox
backend disagrees with this run's own -data-dir or -platform -- stop
the running daemon first, or re-run with flags that match what is
actually running. A grain-daemon.service an earlier run of this command
installed, with the same unit, counts as done.

Exit status: 0 means the deployment is actually up and provisioned; 10
means it ran to completion but one or more steps are manual or refused
(re-run after doing what they say); 1 means a genuine failure (bad
flags, or an internal error).

Flags:
`)
		fs.PrintDefaults()
	}

	repoRoot := fs.String("repo-root", "", "the grain checkout to build from (required)")
	dataDir := fs.String("data-dir", "", "the daemon's -data-dir (required); for -platform="+
		string(hostinit.PlatformK8sContainer)+" there is no shared filesystem with the daemon, so this instead "+
		"names a directory on this machine for the preflight's own scratch manifests")
	dryRun := fs.Bool("dry-run", false, "print the plan; change nothing")
	platform := fs.String("platform", string(hostinit.DefaultPlatform), "deployment shape to bring up: "+
		string(hostinit.PlatformLinuxKVM)+" (a Linux host with /dev/kvm, one VM per sandbox -- the default), "+
		string(hostinit.PlatformLinuxContainer)+" (a Linux host with no /dev/kvm, one docker container per "+
		"sandbox), "+string(hostinit.PlatformK8sContainer)+" (a Kubernetes cluster, one plain pod per sandbox "+
		"-- see the -k8s-* flags below), "+string(hostinit.PlatformK8sGranule)+" (a Kubernetes cluster, one "+
		"privileged pod per sandbox booting a real VM over /dev/kvm -- see the -k8s-* and -k8s-granule-* flags "+
		"below), or "+string(hostinit.PlatformGKESandboxes)+" (an off-cluster GKE Standard sandbox cluster with "+
		"nested virtualization and /var/lib/grain/kubeconfig -- see the -gke-* flags below); see docs/grain-init-platforms.md")

	networkName := fs.String("sandbox-network", hostinit.DefaultSandboxNetworkName,
		"docker network created for sandboxes to join -- granule's own VM containers under -platform="+
			string(hostinit.PlatformLinuxKVM)+", or the sandbox containers themselves under -platform="+
			string(hostinit.PlatformLinuxContainer)+" -- whose gateway becomes -granule-git-proxy-host or "+
			"-container-git-proxy-host respectively")
	runtimeImage := fs.String("granule-runtime-image", hostinit.DefaultGranuleRuntimeImage,
		"tag for the granule runtime image (scripts/granule/build-granule-image.sh)")
	guestBaseImage := fs.String("guest-base-image", hostinit.DefaultGuestBaseImage, "tag for Granulefile.guest-base")
	guestImage := fs.String("guest-image", hostinit.DefaultGuestImage, "tag for Granulefile -- the full sandbox guest")

	grainBinaryPath := fs.String("grain-binary", "", "where to leave the built grain binary (default <repo-root>/bin/grain)")
	useBinary := fs.String("use-binary", "", "run this prebuilt grain binary (a release download, say) instead of "+
		"building one: skips `make build`, so preflight demands neither make nor node/npm, and under -platform="+
		string(hostinit.PlatformLinuxContainer)+" no Go toolchain either")
	granuleBinaryPath := fs.String("granule-binary", "", "where to build the granule CLI to (default <repo-root>/bin/granule)")

	installClaude := fs.Bool("install-claude-cli", true, "install the claude CLI")
	claudeVersion := fs.String("claude-cli-version", hostinit.DefaultClaudeCLIVersion, "claude CLI release to install")
	claudeSHA256Amd64 := fs.String("claude-cli-sha256-amd64", hostinit.DefaultClaudeCLISHA256Amd64, "expected sha256 of the linux-x64 claude release")
	claudeSHA256Arm64 := fs.String("claude-cli-sha256-arm64", hostinit.DefaultClaudeCLISHA256Arm64, "expected sha256 of the linux-arm64 claude release")
	claudeHome := fs.String("claude-home", "", "$HOME for the claude installer (default <data-dir>/claude)")

	githubCredentialName := fs.String("github-credential-name", "", "name the GitHub token is written under (secrets/github/<name>.token)")
	githubTokenFile := fs.String("github-token-file", "", "path to a file holding a GitHub token to write and narrow to -github-repo; empty (with -github-token also empty) skips the credential-ladder step entirely")
	githubToken := fs.String("github-token", "", "deprecated: a GitHub token given directly on the command line -- ps shows it to every other user on this host for as long as this process runs, and shell history keeps a second copy; use -github-token-file instead. Ignored when -github-token-file is also given.")
	var githubRepos stringList
	fs.Var(&githubRepos, "github-repo", "an \"owner/repo\" or \"owner/*\" pattern to point at -github-credential-name's token, repeatable (never \"*\" -- refused)")

	granuleSSHUser := fs.String("granule-ssh-user", hostinit.DefaultGranuleSSHUser, "guest account granule authorizes its key for and the daemon execs as")
	granuleWorkspace := fs.String("granule-workspace", hostinit.DefaultGranuleWorkspace, "working directory tools operate in on each granule VM")

	// Shared between -platform=k8s-container and -platform=k8s-granule
	// -- which cluster, which Deployment/Service, how to reach it. See
	// hostinit.K8sOptions (pkg/hostinit/k8s.go) and
	// hostinit.K8sGranuleOptions (pkg/hostinit/k8s_granule.go) for what
	// each names; only one of the two is ever read, by -platform.
	k8sNamespace := fs.String("k8s-namespace", hostinit.DefaultK8sNamespace, "k8s-container/k8s-granule: the Namespace the manifest's objects are created in (never created or labelled by this command itself -- see -k8s-granule-* below for why)")
	k8sDeployment := fs.String("k8s-deployment", hostinit.DefaultK8sDeployment, "k8s-container/k8s-granule: the Deployment the manifest creates")
	k8sService := fs.String("k8s-service", hostinit.DefaultK8sService, "k8s-container/k8s-granule: the Service the manifest creates")
	k8sContext := fs.String("k8s-context", "", "k8s-container/k8s-granule: --context passed to every kubectl call this command makes (empty uses kubectl's own current context)")
	k8sKubeconfig := fs.String("k8s-kubeconfig", "", "k8s-container/k8s-granule: --kubeconfig passed to every kubectl call this command makes (empty uses kubectl's own default)")
	k8sManagePortForward := fs.Bool("k8s-manage-port-forward", true, "k8s-container/k8s-granule: start a `kubectl port-forward svc/<k8s-service>` before provisioning and stop it after -- false if -ui-addr already reaches the Service some other way (an operator-managed port-forward, an Ingress, an internal route)")
	var k8sNodeSelector stringList
	fs.Var(&k8sNodeSelector, "k8s-node-selector", "k8s-granule only: one \"key=value\" nodeSelector entry pinning every sandbox pod -- and the preflight's own nested-virt check -- to a node pool, repeatable. Leave unset only on a single-pool cluster where every node offers nested virtualization")

	// -platform=k8s-container only; see hostinit.K8sOptions' own doc
	// comment (pkg/hostinit/k8s.go) for what each of these names.
	k8sManifest := fs.String("k8s-manifest", "", "k8s-container only: path to grain.yaml (default <repo-root>/deploy/kubernetes/grain.yaml)")
	k8sNetworkPolicyManifest := fs.String("k8s-networkpolicy-manifest", "", "k8s-container only: path to networkpolicy.yaml (default <repo-root>/deploy/kubernetes/networkpolicy.yaml)")
	k8sSandboxImage := fs.String("k8s-sandbox-image", "", "k8s-container only: the sandbox image the preflight's throwaway pod pulls -- must match -k8s-manifest's own -k8s-sandbox-image, which preflight refuses otherwise (default: the one -k8s-manifest names, else this build's own "+defaultK8sSandboxImage+")")
	k8sRunHostRWXClass := fs.String("k8s-run-host-rwx-class", "", "k8s-container only: the ReadWriteMany StorageClass of the run hosts' shared volume (deploy/kubernetes/runhost-rwx.yaml); a gcsfuse-backed class is refused. Empty keeps them on grain-data, pinned to the daemon's node")
	k8sStorageClass := fs.String("k8s-storage-class", "", "k8s-container only: the StorageClass the preflight's scratch PVC requests (empty tests the cluster's own default class)")
	k8sRequireNetworkPolicy := fs.Bool("k8s-require-network-policy", false, "k8s-container only: this deployment will dispatch network:none tasks, so preflight must confirm the cluster's CNI actually enforces NetworkPolicy, and apply networkpolicy.yaml")

	// -platform=k8s-granule only; see hostinit.K8sGranuleOptions' own
	// doc comment (pkg/hostinit/k8s_granule.go) for what each names.
	k8sGranuleManifest := fs.String("k8s-granule-manifest", "", "k8s-granule only: path to grain-granule.yaml (default <repo-root>/deploy/kubernetes/grain-granule.yaml)")
	k8sGranuleImage := fs.String("k8s-granule-image", daemon.DefaultSandboxImage, "k8s-granule only: the packed guest image the preflight's throwaway pod boots and every real sandbox pod runs -- a self-contained image with its own guest disk and kernel already baked in (the shape Granulefile's own build produces), not a plain sandbox image and not \"granule:vendored\" either (-granule-runtime-image's own default, which has no guest disk and cannot boot at all). Defaults to defaultSandboxImage, the same per-commit guest CI publishes and -platform=linux-kvm's -sandbox-image pulls -- stamped in at build time the way -k8s-sandbox-image's own default is; override it for a guest of your own (a team layer, a different base)")

	// -platform=gke-sandboxes only.
	gkeProject := fs.String("gke-project", hostinit.DefaultGKESandboxesProject, "gke-sandboxes only: GCP project ID for the GKE Standard sandbox cluster")
	gkeLocation := fs.String("gke-location", hostinit.DefaultGKESandboxesLocation, "gke-sandboxes only: GCP zone or region for the GKE Standard sandbox cluster")
	gkeCluster := fs.String("gke-cluster", hostinit.DefaultGKESandboxesCluster, "gke-sandboxes only: GKE Standard cluster name")
	gkeNodePool := fs.String("gke-node-pool", hostinit.DefaultGKESandboxesNodePool, "gke-sandboxes only: nested-virtualization node pool name")
	gkeMachineType := fs.String("gke-machine-type", hostinit.DefaultGKESandboxesMachineType, "gke-sandboxes only: GCE machine type for nested virtualization (e.g. c4-standard-96-lssd or n2-standard-8)")
	gkeDiskType := fs.String("gke-disk-type", "", "gke-sandboxes only: node boot disk type (defaults to hyperdisk-balanced for c4-* and pd-balanced for n2-*)")
	gkeDiskSizeGB := fs.Int("gke-disk-size-gb", hostinit.DefaultGKESandboxesDiskSizeGB, "gke-sandboxes only: node boot disk size in GiB")
	gkeImageType := fs.String("gke-image-type", hostinit.DefaultGKESandboxesImageType, "gke-sandboxes only: GKE node image type (UBUNTU_CONTAINERD required for nested virtualization)")
	gkeNumNodes := fs.Int("gke-num-nodes", hostinit.DefaultGKESandboxesNumNodes, "gke-sandboxes only: initial node count in the nested-virtualization node pool")
	gkeMinNodes := fs.Int("gke-min-nodes", 0, "gke-sandboxes only: autoscaler minimum node count (when -gke-max-nodes > 0)")
	gkeMaxNodes := fs.Int("gke-max-nodes", 0, "gke-sandboxes only: autoscaler maximum node count (0 disables autoscaling)")
	gkeServiceAccount := fs.String("gke-service-account", hostinit.DefaultGKESandboxesServiceAccount, "gke-sandboxes only: least-privilege Kubernetes ServiceAccount name")
	gkeKubeconfigOut := fs.String("gke-kubeconfig-out", hostinit.DefaultGKESandboxesKubeconfig, "gke-sandboxes only: path to write the least-privilege kubeconfig for the off-cluster controller")
	gkeGitProxyURL := fs.String("gke-git-proxy-url", hostinit.DefaultGKESandboxesGitProxyURL, "gke-sandboxes only: git proxy URL verified from the throwaway sandbox")
	gkePackageCacheURL := fs.String("gke-package-cache-url", hostinit.DefaultGKESandboxesPackageCacheURL, "gke-sandboxes only: package cache URL verified from the throwaway sandbox")

	startDaemon := fs.Bool("start-daemon", true, "start the daemon once everything it needs is ready")
	var extraDaemonArgs stringList
	fs.Var(&extraDaemonArgs, "daemon-arg", "extra argument appended to the daemon's own command line, repeatable")
	daemonUser := fs.String("daemon-user", "", "run the daemon as this user (systemd User=) instead of "+hostinit.DefaultDaemonUser+"; grain init get-or-creates it (a system account, no login shell), get-or-adds it to the \"docker\" group, and chowns -data-dir (and -claude-home, if separate) to it, exactly as it does for the default. The one value that opts all the way back out -- no account created, no chown, no User=/Group= line at all -- is the literal \"root\"")
	upgradeAllowedBranches := fs.String("upgrade-allowed-branches", "",
		"comma-separated branches an upgrade of this deployment may install, written into the unit as the daemon's own "+
			"-upgrade-allowed-branches: each a path.Match pattern ('*' never crosses a '/'; e.g. main,release/*), or ** for "+
			"any branch. Unset keeps whatever the grain-daemon.service already on this host passes, and with none there "+
			"writes no flag, leaving the daemon's default (main, candidate, latest and vX.Y.Z tags). Anyone who can push a "+
			"matching branch can have it built and run as this daemon, so ** is an explicit opt-in")
	daemonGroup := fs.String("daemon-group", "", "run the daemon as this group (systemd Group=) instead of -daemon-user's own name; only meaningful alongside -daemon-user")

	uiAddr := fs.String("ui-addr", hostinit.DefaultUIAddr,
		"host:port the daemon's UI/API listens on, written into a host platform's unit (the steps below reach it over loopback) -- "+
			"unset keeps the installed unit's own on a re-run; must match -ui-addr if -daemon-arg overrides it")
	// Accepted and ignored: the daemon signs nobody in, so it has no
	// system admins for init to check or add. A script that still
	// passes these should not stop init.
	_ = fs.String("admin-account", "", "deprecated and ignored: this build is single-tenant only, with no system admins to set up")
	_ = fs.String("admin-user", "", "deprecated and ignored: this build is single-tenant only, with no system admins to set up")
	// Accepted and ignored: this build is single-tenant only, and the
	// daemon has no -tenancy any more. A script that still passes this
	// should not stop init.
	_ = fs.String("tenancy", "", "deprecated and ignored: this build is single-tenant only")
	// Accepted and ignored: a team has no worker count of its own any
	// more, and a script that still passes this should not stop init.
	_ = fs.Int("team-max-workers", 0, "deprecated and ignored: a team has no worker count of its own")
	// Accepted and ignored: the daemon has no -ui-trust-loopback any
	// more, since every caller, loopback included, presents the access
	// code. A script that still passes this should not stop init.
	_ = fs.Bool("ui-trust-loopback", true, "deprecated and ignored: every caller, loopback included, presents the access code")

	if err := fs.Parse(args); err != nil {
		return hostinit.Result{}, err
	}
	uiAddrSet := false
	fs.Visit(func(f *flag.Flag) {
		uiAddrSet = uiAddrSet || f.Name == "ui-addr"
	})
	if *repoRoot == "" {
		return hostinit.Result{}, fmt.Errorf("-repo-root is required")
	}
	if *dataDir == "" {
		return hostinit.Result{}, fmt.Errorf("-data-dir is required")
	}
	if *k8sSandboxImage == "" && hostinit.Platform(*platform) == hostinit.PlatformK8sContainer {
		*k8sSandboxImage = k8sSandboxImageFromManifest(*k8sManifest, *repoRoot)
	}
	// The steps that provision over the loopback API present the local
	// API token the daemon writes into -data-dir, which they can read
	// running as root: every caller, loopback included, presents the
	// access code (pkg/ui/localtoken.go).
	if os.Getenv(ui.LocalAPITokenFileEnv) == "" {
		os.Setenv(ui.LocalAPITokenFileEnv, ui.LocalAPITokenPath(absDataDir(*dataDir)))
	}

	resolvedGitHubToken, err := resolveGitHubToken(*githubToken, *githubTokenFile, os.Stderr)
	if err != nil {
		return hostinit.Result{}, err
	}
	// hostinit.Options.validate refuses the same thing, but in its own
	// field names (GitHubCredentialName, GitHubToken) -- not the flags a
	// person running this command actually typed.
	if resolvedGitHubToken != "" && *githubCredentialName == "" {
		return hostinit.Result{}, fmt.Errorf("-github-credential-name is required with -github-token-file (or -github-token): " +
			"it names secrets/github/<name>.token, the file the token is written to")
	}

	opts := hostinit.Options{
		RepoRoot:               *repoRoot,
		DataDir:                *dataDir,
		DryRun:                 *dryRun,
		Platform:               hostinit.Platform(*platform),
		SandboxNetworkName:     *networkName,
		GranuleRuntimeImage:    *runtimeImage,
		GuestBaseImage:         *guestBaseImage,
		GuestImage:             *guestImage,
		GrainBinaryPath:        *grainBinaryPath,
		UseBinary:              *useBinary,
		GranuleBinaryPath:      *granuleBinaryPath,
		InstallClaudeCLI:       installClaude,
		ClaudeCLIVersion:       *claudeVersion,
		ClaudeCLISHA256Amd64:   *claudeSHA256Amd64,
		ClaudeCLISHA256Arm64:   *claudeSHA256Arm64,
		ClaudeHome:             *claudeHome,
		GitHubCredentialName:   *githubCredentialName,
		GitHubToken:            resolvedGitHubToken,
		GitHubRepos:            githubRepos,
		GranuleSSHUser:         *granuleSSHUser,
		GranuleWorkspace:       *granuleWorkspace,
		StartDaemon:            startDaemon,
		ExtraDaemonArgs:        extraDaemonArgs,
		UpgradeAllowedBranches: daemon.SplitBranchPatterns(*upgradeAllowedBranches),
		DaemonUser:             *daemonUser,
		DaemonGroup:            *daemonGroup,
		UIAddr:                 givenUIAddr(*uiAddr, uiAddrSet),
		K8s: hostinit.K8sOptions{
			Namespace:             *k8sNamespace,
			Deployment:            *k8sDeployment,
			Service:               *k8sService,
			Manifest:              *k8sManifest,
			NetworkPolicyManifest: *k8sNetworkPolicyManifest,
			SandboxImage:          *k8sSandboxImage,
			StorageClass:          *k8sStorageClass,
			RunHostRWXClass:       *k8sRunHostRWXClass,
			RequireNetworkPolicy:  *k8sRequireNetworkPolicy,
			Context:               *k8sContext,
			Kubeconfig:            *k8sKubeconfig,
			ManagePortForward:     k8sManagePortForward,
		},
		K8sGranule: hostinit.K8sGranuleOptions{
			Namespace:         *k8sNamespace,
			Deployment:        *k8sDeployment,
			Service:           *k8sService,
			Manifest:          *k8sGranuleManifest,
			Image:             *k8sGranuleImage,
			NodeSelector:      daemon.ParseKeyValues(k8sNodeSelector),
			Context:           *k8sContext,
			Kubeconfig:        *k8sKubeconfig,
			ManagePortForward: k8sManagePortForward,
		},
		GKESandboxes: hostinit.GKESandboxesOptions{
			Project:         *gkeProject,
			Location:        *gkeLocation,
			Cluster:         *gkeCluster,
			NodePool:        *gkeNodePool,
			MachineType:     *gkeMachineType,
			DiskType:        *gkeDiskType,
			DiskSizeGB:      *gkeDiskSizeGB,
			ImageType:       *gkeImageType,
			NumNodes:        *gkeNumNodes,
			MinNodes:        *gkeMinNodes,
			MaxNodes:        *gkeMaxNodes,
			Namespace:       *k8sNamespace,
			ServiceAccount:  *gkeServiceAccount,
			KubeconfigPath:  *gkeKubeconfigOut,
			GuestImage:      *k8sGranuleImage,
			GitProxyURL:     *gkeGitProxyURL,
			PackageCacheURL: *gkePackageCacheURL,
		},
	}

	result, err := hostinit.Init(ctx, hostinit.RealSystem{}, opts)
	if err != nil {
		return hostinit.Result{}, err
	}
	printInitResult(result, opts)
	return result, nil
}

// k8sSandboxImageFromManifest is -k8s-sandbox-image's default: the image
// the manifest grain init is about to apply starts the daemon with, so
// that the preflight pulls the image every task will actually run on.
// This build's own stamped default stood in for it before, and an
// unstamped local build's :latest is not grain.yaml's :main. Only a
// manifest that names none -- whose daemon then does run its own
// build's default -- or one that cannot be read (the apply step says
// so) falls back to that.
func k8sSandboxImageFromManifest(manifest, repoRoot string) string {
	if manifest == "" {
		manifest = filepath.Join(repoRoot, "deploy", "kubernetes", "grain.yaml")
	}
	if image, err := hostinit.ManifestK8sSandboxImage(manifest); err == nil && image != "" {
		return image
	}
	return defaultK8sSandboxImage
}

// manualFollowUps is one line per [manual] step for printInitResult's
// "Run these" list: the step's command, or -- for a step that has none
// to offer, such as missing RBAC that only a cluster admin can grant --
// a pointer back to its detail, rather than the blank line it used to
// print.
func manualFollowUps(manual []hostinit.Step) []string {
	lines := make([]string, 0, len(manual))
	for _, s := range manual {
		if strings.TrimSpace(s.Command) == "" {
			lines = append(lines, fmt.Sprintf("(%s: no command -- see its line above)", s.Name))
			continue
		}
		lines = append(lines, s.Command)
	}
	return lines
}

func printInitResult(result hostinit.Result, opts hostinit.Options) {
	fmt.Println("grain init:")
	var manual []hostinit.Step
	daemonUp := false
	for _, s := range result.Steps {
		if s.Name == "start the daemon" {
			daemonUp = s.Status == hostinit.StepDone || s.Status == hostinit.StepSkipped
		}
		switch s.Status {
		case hostinit.StepDone:
			fmt.Printf("  [done]    %-55s %s\n", s.Name, s.Detail)
		case hostinit.StepSkipped:
			fmt.Printf("  [skipped] %-55s %s\n", s.Name, s.Detail)
		case hostinit.StepWould:
			fmt.Printf("  [would]   %-55s %s\n", s.Name, s.Detail)
		case hostinit.StepManual:
			fmt.Printf("  [manual]  %-55s %s\n", s.Name, s.Detail)
			manual = append(manual, s)
		case hostinit.StepRefused:
			fmt.Printf("  [refused] %-55s %s\n", s.Name, s.Detail)
		}
		if s.Command != "" && !strings.Contains(s.Detail, s.Command) {
			fmt.Printf("            $ %s\n", s.Command)
		}
	}
	if len(manual) > 0 {
		fmt.Println("\nSome steps are not done yet -- either they need more access than this ran with, or " +
			"grain init will never perform them itself. Run these, then re-run this command:")
		for _, line := range manualFollowUps(manual) {
			fmt.Printf("  %s\n", line)
		}
	}
	if result.SandboxGitProxyHost != "" {
		flagName := "-granule-git-proxy-host"
		if opts.Platform == hostinit.PlatformLinuxContainer {
			flagName = "-container-git-proxy-host"
		}
		fmt.Printf("\n%s resolved to %s\n", flagName, result.SandboxGitProxyHost)
	}
	// Every caller presents the access code, this host's CLI included.
	if daemonUp {
		daemonUser := opts.DaemonUser
		if daemonUser == "" {
			daemonUser = hostinit.DefaultDaemonUser
		}
		dataDir := opts.DataDir
		fmt.Println("\nTo run the grain CLI against this daemon, authenticate with the local API token:")
		fmt.Printf("  sudo -u %s env GRAIN_DATA_DIR=%s grain <command>\n", daemonUser, dataDir)
	}
}

// givenUIAddr is -ui-addr as hostinit.Options.UIAddr takes it: empty
// when the flag was not given, so a re-run keeps the address the
// installed unit already uses rather than move it to the default.
func givenUIAddr(addr string, set bool) string {
	if !set {
		return ""
	}
	return addr
}
