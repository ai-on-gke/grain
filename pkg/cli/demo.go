// demo.go implements `grain demo`, formerly `grain ui -demo` before
// bwsalmon/agents#363 folded the real UI into `grain daemon` and left
// nothing standalone for the CLI's own store flag (-data-dir) to attach
// to. This mode never needed that anyway -- its whole point, unchanged,
// is a throwaway store nothing else is connected to (pkg/demo's
// seedDemo doc comment) -- so it is its own small subcommand now rather
// than a flag on a mode that no longer exists: a real pkg/ui.Server, over
// a real embedded SQLite database in a fresh temp directory, seeded with
// fake tasks, for trying out the frontend with no daemon, no
// orchestrator, no sandbox, no Gemini key and no real git repo behind any
// of it.
//
// The demo itself -- the store, the seeding and every opt-in scenario --
// is pkg/demo; this file is only the command's flags.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/demo"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ports"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func cmdDemo(args []string) {
	opts, err := parseDemoFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "grain demo: %v\n", err)
		os.Exit(2)
	}
	log.Fatal(demo.Run(opts))
}

func parseDemoFlags(args []string) (demo.Options, error) {
	fs := flag.NewFlagSet("grain demo", flag.ContinueOnError)
	addr := fs.String("addr", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports.Demo)), "address to serve the demo UI on")
	actor := fs.String("as", "", "principal to attribute the fake tasks to (defaults to the OS user)")
	defaultTargetRepo := fs.String("default-target-repo", "acme/widgets", "owner/name the fake tasks target")
	open := fs.Bool("open", true, "open the UI in the system's default browser once it's listening")
	observability := fs.Bool("observability", false, "serve the live CI and sandbox observability demo")
	staging := fs.Bool("staging", false, "run two fake persistent sandboxes, one on a /staging/ sub-path and one on a host port, for the rail's Staging group")
	unconfigured := fs.Bool("unconfigured", false, "serve a deployment with no agent model and no git credential configured, as a fresh install has")
	agentPaused := fs.Bool("agent-paused", false, "serve a deployment whose agent dispatch is paused on a provider's usage limit")
	mergeQueueStates := fs.Bool("merge-queue-states", false, "also seed two pull requests the merge queue is waiting on: one for its base branch to go green, one for an approval GitHub requires")
	fakeCI := fs.Bool("fake-ci", false, "wire a grain CI stub that accepts and reports runs but never executes anything")
	githubSim := fs.Bool("github-sim", false, "wire a simulated GitHub whose -default-target-repo has branches main and release/1.0")
	scheduledWorkflows := fs.Bool("scheduled-workflows", false, "with -github-sim and -fake-ci, add a candidate branch to the simulated repo, read its .grain declarations and start and settle its workflow schedules' runs every -reconcile interval (default 2s), and let POST /api/e2e/fake-ci/runs/{id}/finish end a run")
	githubSimPublic := fs.Bool("github-sim-public", false, "with -git-proxy, let the simulated GitHub serve its repo to an anonymous fetch, as GitHub does a public repository; an anonymous push is still refused")
	gitProxy := fs.Bool("git-proxy", false, "with -github-sim, serve grain's git proxy in front of the simulated repo on loopback, and POST /api/e2e/git-proxy/runs to start a task's run and mint its sandbox a token to push with")
	baseBranchCI := fs.Bool("base-branch-ci", false, "with -github-sim and -fake-ci, let POST /api/e2e/base-branch/{red,green} {\"branch\": ...} (loopback only) turn a branch red or green on a -fake-ci verdict, through grain's own base-branch reconciler and dispatch's linking step, dispatching nothing")
	mergeRules := fs.Bool("merge-rules", false, "seed acme/widgets' merge rules (the docs/declarations.md example) and two deploy/** changes the merge queue classified under them")
	mechanicalPort := fs.Bool("mechanical-port", false, "seed a change merged onto main and the port grain cherry-picked to candidate itself, with no agent run")
	qualification := fs.Bool("qualification", false, "seed a release candidate of -default-target-repo whose qualification run is waiting for approval")
	secretsStore := fs.Bool("secrets-store", false, "wire a real secrets store in the demo's throwaway directory, so secrets and release secrets can be set and removed")
	reviewQuestions := fs.Bool("review-questions", false, "seed a change whose review left questions for its owner, held on their decision, and send it back to the queue once they reply, as a daemon would")
	externalConfigRepo := fs.String("external-config-repo", "", "serve a deployment whose configuration is managed in this external config repository (a remote URL): every configuration write is refused with 409 and the exact edit to commit there instead (docs/external-config-repo.md)")
	externalConfigBranch := fs.String("external-config-branch", "main", "with -external-config-repo, the branch of that repository configuration is read from")
	sensitiveHold := fs.Bool("sensitive-hold", false, "seed a change held by a sensitive path trigger, and a failed trigger declaration read")
	githubAPI := fs.String("github-api", "", "host:port of a GitHub API stub on loopback, spoken to over plain HTTP: the Settings → Git Test buttons, the New task pickers and filing's reach refusal all ask it what the deployment's credential can reach")
	upgradeImage := fs.String("upgrade-image", "", "wire a real image-mode upgrader pulling from this repository (localhost:5000/x/grain): the Upgrade tab lists its tags and an upgrade really pulls, verifies and health-checks the image, but never restarts anything")
	upgradeAllowedBranches := fs.String("upgrade-allowed-branches", strings.Join(upgrade.DefaultAllowedBranches, ","), "comma-separated branches (path.Match patterns, or ** for any) -upgrade-image may upgrade to")
	modelCatalog := fs.String("model-catalog", "", "host:port of an Anthropic model listing stub (GET /v1/models) on loopback: Claude connections that advertise no models are given what it lists, as a deployment's are given what Anthropic lists")
	modelDiscoveryInterval := fs.Duration("model-discovery-interval", 0, "how often -model-catalog's discovery asks the provider again (default the deployment's 10m)")
	hostExecutor := fs.String("host-executor", "", "resolve Settings -> Models' agent CLI run locations as this deployment would: "+
		"empty for a development daemon on the bare machine, \"kubernetes\" (no host executor), \"none\" (a container with no grain hostd) or "+
		"\"hostd-down\" (a container whose grain hostd is configured but not answering) or \"hostd\" (a container with a real grain hostd "+
		"on a throwaway allowlist offering agy)")
	hostModeInstall := fs.Bool("host-mode-install", false, "show the notice an unmigrated host-mode install shows (banner, System -> Health, Upgrade), with the CLIs the default bare-machine run locations leave nowhere to run")
	schedules := fs.Bool("schedules", false, "run the reconcile loop's schedule step every -reconcile interval (default 2s), so a schedule that comes due fires, or records why it was skipped, on its own (what it files is still never dispatched)")
	reconcile := fs.Duration("reconcile", 0, "run the reconcile loop's suite step every interval, and its release step against -github-sim's simulated GitHub too, logging a failed cycle as the daemon does (default off; agents still never run)")
	var upgradeVerifyCmd stringSliceFlag
	fs.Var(&upgradeVerifyCmd, "upgrade-verify-cmd", "one argument of the command that verifies an -upgrade-image image's signature, repeated for every argument, as on grain daemon")
	accessCodeFile := fs.String("access-code-file", "", "keep the demo's access code, which every caller, loopback included, signs in with, in this file -- written there if it is not already -- rather than beside its throwaway store, where it is logged")
	if err := fs.Parse(args); err != nil {
		return demo.Options{}, err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return demo.Options{}, err
	}

	if *upgradeImage == "" {
		set := false
		fs.Visit(func(f *flag.Flag) {
			set = set || f.Name == "upgrade-allowed-branches" || f.Name == "upgrade-verify-cmd"
		})
		if set {
			return demo.Options{}, errors.New("-upgrade-allowed-branches and -upgrade-verify-cmd only mean something with -upgrade-image")
		}
	}
	if *externalConfigRepo == "" {
		set := false
		fs.Visit(func(f *flag.Flag) { set = set || f.Name == "external-config-branch" })
		if set {
			return demo.Options{}, errors.New("-external-config-branch only means something with -external-config-repo")
		}
	}
	if *githubAPI != "" && !demo.IsLoopbackHostPort(*githubAPI) {
		return demo.Options{}, errors.New("-github-api must be a loopback host:port (it is spoken to over plain HTTP)")
	}
	if *modelCatalog != "" && !demo.IsLoopbackHostPort(*modelCatalog) {
		return demo.Options{}, errors.New("-model-catalog must be a loopback host:port (it is spoken to over plain HTTP)")
	}
	if *fakeCI && *observability {
		return demo.Options{}, errors.New("-fake-ci and -observability each wire a CI of their own; pick one")
	}
	if *reconcile < 0 {
		return demo.Options{}, errors.New("-reconcile must not be negative")
	}
	repo, err := model.ParseRepo(*defaultTargetRepo)
	if err != nil {
		return demo.Options{}, fmt.Errorf("-default-target-repo: %w", err)
	}
	return demo.Options{
		AccessCodeFile:         *accessCodeFile,
		Addr:                   *addr,
		Actor:                  *actor,
		Repo:                   repo,
		Open:                   *open,
		Observability:          *observability,
		Staging:                *staging,
		Unconfigured:           *unconfigured,
		AgentPaused:            *agentPaused,
		MergeQueueStates:       *mergeQueueStates,
		FakeCI:                 *fakeCI,
		GitHubSim:              *githubSim,
		ScheduledWorkflows:     *scheduledWorkflows,
		GitProxy:               *gitProxy,
		BaseBranchCI:           *baseBranchCI,
		GitHubSimPublic:        *githubSimPublic,
		MergeRules:             *mergeRules,
		SensitiveHold:          *sensitiveHold,
		ReviewQuestions:        *reviewQuestions,
		ExternalConfigRepo:     *externalConfigRepo,
		ExternalConfigBranch:   *externalConfigBranch,
		MechanicalPort:         *mechanicalPort,
		Qualification:          *qualification,
		SecretsStore:           *secretsStore,
		GitHubAPI:              *githubAPI,
		UpgradeImage:           *upgradeImage,
		UpgradeAllowedBranches: daemon.SplitBranchPatterns(*upgradeAllowedBranches),
		UpgradeVerifyCmd:       upgradeVerifyCmd,
		ModelCatalog:           *modelCatalog,
		ModelDiscoveryInterval: *modelDiscoveryInterval,
		Schedules:              *schedules,
		Reconcile:              *reconcile,
		HostExecutor:           *hostExecutor,
		HostModeInstall:        *hostModeInstall,
	}, nil
}
