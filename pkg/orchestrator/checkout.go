package orchestrator

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

// CheckoutDir is where prepareCheckout leaves a dispatched task's clone,
// relative to the sandbox's own working directory -- the same "work" the
// scripted agents in e2e have always cloned into by hand, kept so a
// transcript from before this existed still reads the same way.
const CheckoutDir = "work"

// DirectorStateDir is the documented, read-only checkout of the deployment's
// state repository made available to Director sessions.
const DirectorStateDir = "grain-state"

type directorStateStatus string

const (
	directorStateAbsent       directorStateStatus = "absent"
	directorStateUnauthorized directorStateStatus = "unauthorized"
	directorStateUnavailable  directorStateStatus = "unavailable"
	directorStateReady        directorStateStatus = "ready"
)

// GrainSourceDir is where a self-debug run finds the deployment's own
// baked-in copy of grain's source, read-only (placeGrainSource).
const GrainSourceDir = "grain-src"

type grainSourceStatus string

const (
	grainSourceAbsent      grainSourceStatus = "absent"
	grainSourceNotBaked    grainSourceStatus = "not-baked"
	grainSourceUnavailable grainSourceStatus = "unavailable"
	grainSourceReady       grainSourceStatus = "ready"
)

// A sandbox starts empty. HostSandboxes hands out a bare directory
// (orchestrator.go's own doc comment) and GranuleSandboxes a fresh VM;
// neither has the task's repo in it, and until prepareCheckout the agent
// was left to work that out for itself from a prompt that never said so
// -- BuildPrompt names the repo and the branch to push, and mentions
// cloning only for the read-only repos in task.Reads. A live run showed
// what that costs: the agent's first tool call was a git command in the
// empty directory, which failed with "not a git repository", and it gave
// up there rather than cloning. The work only happened on the redispatch
// after it, which had a sandbox with the previous attempt's leftovers in
// it to go on.
//
// It also had no way to know where to clone *from*. The remote is not
// github.com but this deployment's own git proxy (pkg/cli/daemon.go's
// startGitProxy), which is what holds the credential the sandbox
// authenticates with and what refuses a push to anything but the task's
// target (gitproxy/authorize.go) -- an address that reaches the agent
// nowhere else, since ConfigureGitCredentials writes only the proxy's
// host into .git-credentials, never a URL to clone.
//
// So the clone happens here, before the agent's first turn, against the
// proxy URL the daemon already has in hand.

// gitSafe is what an owner, repo name, branch or base has to match to be
// interpolated into the shell script below. Everything reaching it is
// grain's own (model.BranchName) or came through directives.go's own
// `/repo`/`/base` parse, which accepts any non-space token -- so a task
// body could otherwise put a quote or a backtick into a command this
// package composes. The sandbox is where the agent runs arbitrary shell
// anyway, so this is not a privilege boundary; it is the difference
// between a bad ref failing with git's own error and a bad ref producing
// a command that does something else entirely.
var gitSafe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// CloneURL is the address a sandbox clones repo from: not github.com, but
// base -- this deployment's git proxy, which authenticates the sandbox by
// its own token and authorizes what it may fetch and push. A repository
// on a git host other than the default carries its host in the path
// (base/host/owner/name.git), which is how the proxy knows where to
// route it (docs/git-hosts.md); one on the default host keeps the
// owner/name.git path it always had.
func CloneURL(base string, repo model.RepoRef) string {
	return strings.TrimSuffix(base, "/") + "/" + repo.String() + ".git"
}

func checkoutURL(base, configured string, repo model.RepoRef) string {
	// grain/config is not an external repository.
	// Older deployments may still carry the mirror URL they used before the config
	// repository became controller-owned; never let that stale value replace its stable local
	// alias on either initial checkout or sandbox recreation.
	if model.IsConfigRepo(repo) {
		return CloneURL(base, repo)
	}
	if configured != "" {
		return configured
	}
	return CloneURL(base, repo)
}

// checkout is what prepareCheckout left in a sandbox: the clone, and
// what the repo's own setup command did to it.
type checkout struct {
	// Dir is where the clone landed, relative to the sandbox's working
	// directory (CheckoutDir), or "" when there was nothing to clone --
	// a task with no target, or a deployment running no git proxy.
	Dir string
	// Root is the sandbox's own working directory as an absolute path
	// inside it -- where run_command starts, and therefore the directory
	// Dir hangs off. "" when the sandbox could not be asked (see
	// sandboxWorkingDir), which is what a slot exposing no run_command
	// tool at all gets.
	//
	// It is discovered rather than assumed because nothing in this
	// package knows it: a HostSandboxes slot is a directory on the
	// controller and a granule slot is a path inside a guest, and only
	// the mcpserver process holding one end of the tool call has ever
	// been told which. BuildPrompt is the one thing that reads it, and
	// sandboxDirsSection is where it says why a prompt that cannot name
	// this path costs its run turns.
	Root string
	// StateRepo reports that what was cloned is a grain state repository
	// (pkg/staterepo): a dump of grain's own database as text, rather
	// than source code. It decides whether the prompt carries
	// stateRepoSection, and qualifies what settingsRepoSection says
	// beside it; nothing else reads it.
	//
	// Answered by what is in the checkout -- a tables/ directory beside
	// a schema-version stamp -- rather than by comparing the target
	// against this deployment's configured state remote. The layout is
	// the thing the agent has to be told about, and it is the same
	// layout whether the repository is this deployment's own state, some
	// other grain's, or a copy somebody is editing offline.
	//
	// Which of those three it is, is a separate question with a separate
	// answer: Config.StateRepo, the repository this deployment actually
	// reads its settings out of, compared against the task's target in
	// settingsRepoSection. The two sit side by side in the prompt --
	// what the tree is, and whose it is -- because neither substitutes
	// for the other: a dump this deployment does not read still has to
	// be described as a dump, and a settings repository grain has not
	// exported into yet is still this deployment's settings.
	StateRepo bool
	// Setup is what model.RepoConfig.SetupCommand did in that directory,
	// or nil when the repo configures none -- which is every repo until
	// somebody writes one. A setup that *failed* is still a non-nil
	// Setup and not an error: see runSetupCommand.
	Setup *SetupResult
	// DirectorState records the best-effort checkout made for a Director
	// session. It never makes dispatch fail: the prompt explains why the
	// state is absent when it cannot be supplied.
	DirectorState     directorStateStatus
	DirectorStateRepo model.RepoRef
	ProxyBase         string
	// GrainSource records what placeGrainSource did with the
	// deployment's baked-in copy of grain's source for a self-debug run.
	GrainSource grainSourceStatus
	// GrainSourceRevision is the commit that copy was exported at ("" when
	// neither its history nor this binary's stamp says), and
	// GrainSourceCommits how many commits of shallow history came with
	// it -- 0 for a copy baked without any.
	GrainSourceRevision string
	GrainSourceCommits  int
	// GrainSourceWhy is why placing it failed, for the prompt's note on
	// why there is no grain-src/.
	GrainSourceWhy string
}

// SetupResult is one run of a repo's setup command: what was run, how it
// ended, and enough of what it printed to act on.
//
// It exists to be told to the agent (setupSection, in run.go, and
// restoreCheckout's own warning on the recreate path). Nothing else
// reads it, and nothing here decides anything on it -- a failed setup is
// the run's own problem to work around, which it can only do if it is
// told.
//
// Exported for the one reason the checkout that carries it is not:
// BuildPrompt takes one, and BuildPrompt is what a caller outside this
// package (cmd/grain's `demo`) already builds a sample prompt with.
type SetupResult struct {
	// Command is the shell that was run, verbatim, so a prompt can name
	// it rather than describing "the setup command" the run cannot see.
	Command string
	// ExitCode is the status it ended on, read off the exit= line both
	// run_command transports produce (mcp.formatRunCommandResult). -1
	// when that line could not be read at all, which is also what the
	// local transport reports for a command that never started.
	ExitCode int
	// Output is the tail of what it printed -- run_command's whole
	// answer, exit line and both streams, cut to setupOutputBudget from
	// the end. The end is the part worth keeping: a build's error is its
	// last lines, not its first.
	Output string
}

// failed reports whether the setup command ended on anything but a clean
// exit -- the one thing a reader of this has to branch on.
func (r *SetupResult) failed() bool { return r != nil && r.ExitCode != 0 }

// setupCommandTimeout bounds a repo's setup command, and is the line
// between "the agent's problem" and "a failed dispatch": a setup that
// exits, however badly, is reported into the prompt and the run goes
// ahead, while one still running at this bound ends the dispatch before
// an agent is ever started.
//
// The two are treated differently because they fail differently. A
// setup that exits non-zero has told grain and the run everything there
// is to know, and the run may well be able to work around it -- or be
// the very task filed to fix it. A setup that does not finish has told
// nobody anything and would otherwise spend the whole of a run's
// wall-clock budget inside a single tool call the agent cannot see, at
// the end of which the sandbox is destroyed with nothing to show.
// Failing the dispatch instead leaves a run whose detail names the
// timeout, which is a thing a human can act on.
//
// Ten minutes is mcp's own maxRunCommandTimeout, the ceiling run_command
// clamps any caller's bound to, so this asks for exactly as long as a
// sandbox tool call can ever last. A var, not a const, only so a test
// can shrink it rather than actually wait it out.
var setupCommandTimeout = 10 * time.Minute

// setupOutputBudget is how much of the setup command's output reaches
// the prompt. Enough for a compiler's or a package manager's last words,
// far short of the whole log of a build that printed for ten minutes --
// which would cost the run more context than the failure is worth, and
// bury the task it was actually given.
const setupOutputBudget = 4000

// prepareCheckout clones task.Target into CheckoutDir inside the slot's
// sandbox, leaves task's own branch checked out, and runs the repo's own
// setup command (setup, model.RepoConfig.SetupCommand -- "" for the
// repos that need none) in the result, returning what it prepared: a
// zero checkout when there is nothing to prepare, which is what a
// deployment or test with no proxy URL configured gets.
//
// It runs through the sandbox's own run_command tool rather than
// exec.Command or SSH, which is what makes it work for either backend:
// the tool a HostSandboxes slot exposes runs `bash -c` in that slot's
// directory, and the one a GranuleSandboxes slot exposes runs it inside
// that slot's VM (mcp/ssh_tools.go), so this is the one call that is
// already the right call in both places.
//
// An existing remote branch for this task is checked out and continued
// rather than branched over: a redispatch is usually the second attempt
// at the same task, and its push is a fast-forward of what the first
// attempt pushed instead of a rejected non-fast-forward.
//
// notes is how the two waits in here reach the task's own row while
// nobody is driving the run yet (setupNotes) -- the clone itself is
// stamped by RunDispatch before it calls this, and the setup command,
// which can run for minutes more, by runSetupCommand below. The zero
// value narrates nothing, which is what the recreate path passes: see
// restoreCheckout.
func prepareCheckout(ctx context.Context, tools []mcp.Tool, remoteBase string, task model.Task,
	setup string, notes setupNotes, settingsRepos ...model.RepoRef) (checkout, error) {
	return prepareCheckoutFrom(ctx, tools, remoteBase, "", task, setup, notes, settingsRepos...)
}

func prepareCheckoutFrom(ctx context.Context, tools []mcp.Tool, remoteBase, cloneURL string, task model.Task,
	setup string, notes setupNotes, settingsRepos ...model.RepoRef) (checkout, error) {

	var settings model.RepoRef
	if len(settingsRepos) != 0 {
		settings = settingsRepos[0]
	}
	if id, ok := model.ConsoleSessionIDOf(task); ok && id == model.DirectorSessionID && task.Target == nil {
		return prepareDirectorState(ctx, tools, remoteBase, task, settings), nil
	}

	if (remoteBase == "" && cloneURL == "") || task.Target == nil {
		// Nothing to clone -- but a task with no repo attached still
		// works in a sandbox, and is still told the wrong thing about
		// where by its own framework, so the one question worth asking
		// is asked on its own here. The clone below carries it in its
		// own script instead, rather than spending a second round trip
		// (and a second entry in the narration setupNotes keeps) on it.
		out := checkout{Root: sandboxWorkingDir(ctx, tools)}
		return out, nil
	}
	branch := task.BranchName()
	for _, v := range []string{task.Target.Owner, task.Target.Name, branch} {
		if !gitSafe.MatchString(v) {
			return checkout{}, fmt.Errorf("orchestrator: %q is not a usable git name", v)
		}
	}
	if task.Base != "" && !gitSafe.MatchString(task.Base) {
		return checkout{}, fmt.Errorf("orchestrator: %q is not a usable base branch", task.Base)
	}

	// The base is checked out only in the branch-does-not-exist-yet case:
	// with the branch already on the remote, its own history is what the
	// next attempt continues, and re-rooting it on the base is exactly
	// what would throw the first attempt's commits away.
	//
	// Its existence, though, is checked either way -- see baseCheck.
	rootOnBase := ""
	if task.Base != "" {
		rootOnBase = fmt.Sprintf("git checkout --quiet '%s'\n  ", task.Base)
	}
	//
	// An empty repository -- no commits, so no branches and no default
	// branch to be the base -- clones fine and has nothing to check out:
	// the task's branch is started unborn, so the run's first commit is
	// the repository's first and its push creates the branch, rather than
	// the clone failing on a base that "may have been merged and deleted".
	script := fmt.Sprintf(`set -e
%[7]s
rm -rf '%[1]s'
%[10]s
cd '%[1]s'
if [ -z "$(git for-each-ref --count=1 refs/remotes/origin)" ]; then
  echo "%[9]s %[8]s is an empty repository (no commits and no default branch): starting '%[3]s' unborn, so this run's first commit is the repository's first" >&2
  git symbolic-ref HEAD 'refs/heads/%[3]s'
else
%[4]sif git rev-parse --verify --quiet 'refs/remotes/origin/%[3]s' >/dev/null; then
  git checkout --quiet -b '%[3]s' 'origin/%[3]s'
else
  %[5]sgit checkout --quiet -b '%[3]s'
fi
fi
%[6]s`, CheckoutDir, shellQuote(checkoutURL(remoteBase, cloneURL, *task.Target)), branch, baseCheck(task, branch), rootOnBase,
		stateRepoCheck(), sandboxRootEcho(), task.Target, baseGoneMarker,
		RetryingCloneScript(shellQuote(checkoutURL(remoteBase, cloneURL, *task.Target)), shellQuote(CheckoutDir)))

	run, ok := runCommandTool(tools)
	if !ok {
		return checkout{}, fmt.Errorf("orchestrator: slot's sandbox exposes no run_command tool to clone %s with", task.Target)
	}
	result := run(ctx, map[string]any{"command": script})
	if result.IsError {
		err := fmt.Errorf("orchestrator: cloning %s into %s: %s",
			task.Target, CheckoutDir, strings.TrimSpace(result.Text))
		if baseGoneFatally(result.Text, task.Base) {
			return checkout{}, &baseGoneError{Repo: *task.Target, Base: task.Base, err: err}
		}
		if gitproxy.IsAccessRefusal(result.Text) {
			return checkout{}, &accessRefusedError{Repo: *task.Target, Output: strings.TrimSpace(result.Text), err: err}
		}
		return checkout{}, err
	}
	// The survivable half of baseCheck: the run is going ahead on a branch
	// that already exists, and the only record of what this found is
	// whatever it printed, which nothing else reads. Said in the daemon's
	// own journal so that the first place the missing base turns up is the
	// start of the attempt rather than the pull request refused at the end
	// of it. Only grain's own lines, never git's chatter: the marker is a
	// prefix this package composed a few lines up.
	for _, line := range strings.Split(result.Text, "\n") {
		if said, ok := strings.CutPrefix(strings.TrimSpace(line), baseGoneMarker); ok {
			log.Printf("orchestrator: task %s: %s", task.ID, strings.TrimSpace(said))
		}
	}
	out := checkout{
		Dir:       CheckoutDir,
		Root:      sandboxRootFrom(result.Text),
		StateRepo: strings.Contains(result.Text, stateRepoMarker),
	}
	ran, err := runSetupCommand(ctx, run, task, setup, notes)
	if err != nil {
		return checkout{}, err
	}
	out.Setup = ran
	return out, nil
}

func prepareDirectorState(ctx context.Context, tools []mcp.Tool, remoteBase string, task model.Task, repo model.RepoRef) checkout {
	out := checkout{DirectorStateRepo: repo, ProxyBase: proxyBaseWithoutCredentials(remoteBase)}
	if repo.Owner == "" {
		out.DirectorState = directorStateAbsent
		out.Root = sandboxWorkingDir(ctx, tools)
		return out
	}
	granted := false
	for _, read := range task.Reads {
		if read == repo {
			granted = true
			break
		}
	}
	if !granted {
		out.DirectorState = directorStateUnauthorized
		out.Root = sandboxWorkingDir(ctx, tools)
		return out
	}
	run, ok := runCommandTool(tools)
	if !ok || out.ProxyBase == "" {
		out.DirectorState = directorStateUnavailable
		out.Root = sandboxWorkingDir(ctx, tools)
		return out
	}
	script := fmt.Sprintf(`%s
rm -rf '%s'
git clone --quiet '%s' '%s' && git -C '%s' remote set-url --push origin grain-read-only`,
		sandboxRootEcho(), DirectorStateDir, CloneURL(out.ProxyBase, repo), DirectorStateDir, DirectorStateDir)
	result := run(ctx, map[string]any{"command": script})
	out.Root = sandboxRootFrom(result.Text)
	if result.IsError {
		out.DirectorState = directorStateUnavailable
		log.Printf("orchestrator: director task %s: state repository checkout unavailable: %s", task.ID, strings.TrimSpace(result.Text))
		return out
	}
	out.DirectorState = directorStateReady
	return out
}

// buildRevision is what a revision stamp has to look like to be put in a
// shell command: a full or abbreviated commit hash, nothing else.
var buildRevision = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// proxyBaseWithoutCredentials keeps provisioning's reachable endpoint while
// ensuring neither a prompt nor a git remote can disclose URL userinfo.
func proxyBaseWithoutCredentials(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" {
		return strings.TrimRight(strings.TrimSpace(base), "/")
	}
	u.User = nil
	return strings.TrimRight(u.String(), "/")
}

// runSetupCommand runs this repo's setup command in the checkout that
// was just cloned, through the same run_command handler the clone above
// went through -- so it works on either sandbox backend for the reason
// prepareCheckout's own doc comment gives.
//
// nil, nil for a repo with no setup command, which is the ordinary case.
//
// A command that exits non-zero comes back as a SetupResult with that
// status in it and no error at all: the run goes ahead and is told what
// happened (setupSection). Hiding it would leave an agent working in a
// tree that does not build with no idea why, which is exactly the state
// this field exists to prevent -- and grain cannot know whether a failed
// `make deps` is fatal to the task in hand, whereas the run can find out
// in one command.
//
// The one error it does return is the bound: a command still running at
// setupCommandTimeout fails the dispatch, which that var's own doc
// comment explains.
func runSetupCommand(ctx context.Context, run func(context.Context, map[string]any) mcp.Result,
	task model.Task, setup string, notes setupNotes) (*SetupResult, error) {

	setup = strings.TrimSpace(setup)
	if setup == "" {
		return nil, nil
	}
	// Said only once there is something to say it about: a repo with no
	// setup command must not leave "running the repo's setup command"
	// standing on the row for the rest of the run's setup.
	notes.say(ctx, setupCommandNote)
	// cd first, in its own line, so a setup command is written the way
	// somebody would type it at the top of the checkout rather than
	// having to know where grain put the clone. `set -e` is deliberately
	// not applied over it: the command is whoever wrote it's to compose,
	// and a multi-line setup that means `a || b` should get it.
	script := fmt.Sprintf("cd '%s'\n%s", CheckoutDir, setup)
	started := time.Now()
	result := run(ctx, map[string]any{
		"command": script,
		"timeout": float64(setupCommandTimeout / time.Millisecond),
	})
	// The bound, told apart from an ordinary failure by the clock rather
	// than by the text: both transports answer a killed command with a
	// non-zero status like any other (mcp/run_command_result.go), and the
	// only thing that distinguishes "the bound ended it" is that it ran
	// for the whole of it.
	if elapsed := time.Since(started); elapsed >= setupCommandTimeout {
		return nil, fmt.Errorf(
			"orchestrator: %s's setup command was still running after %s and was killed; "+
				"no agent was started (%s)",
			task.Target, humanDuration(setupCommandTimeout), setup)
	}
	return &SetupResult{
		Command:  setup,
		ExitCode: runCommandExitCode(result),
		Output:   tailBytes(strings.TrimSpace(result.Text), setupOutputBudget),
	}, nil
}

// runCommandExitCode reads the status off a run_command answer, whose
// first line is `exit=N` on both transports
// (mcp.formatRunCommandResult). -1 for an answer that has no such line,
// which is what a tool refusing the call outright looks like -- the same
// number the local transport already uses for a command that could not
// be started.
func runCommandExitCode(result mcp.Result) int {
	first, _, _ := strings.Cut(result.Text, "\n")
	if code, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(first, "exit="))); err == nil &&
		strings.HasPrefix(first, "exit=") {
		return code
	}
	if result.IsError {
		return -1
	}
	return 0
}

// tailBytes cuts s to its last budget bytes, on a line boundary, and
// says that it did. The last lines are the ones worth keeping: what a
// failed build printed as it gave up, rather than the package list it
// started with.
func tailBytes(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	cut := s[len(s)-budget:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 {
		cut = cut[i+1:]
	}
	return "[grain] earlier output omitted\n" + cut
}

// commitMarker prefixes each commit line checkoutCommits has git print, so
// those lines can be picked back out of a run_command result that also
// carries the tool's own `exit=`/`stdout:`/`stderr:` framing and whatever
// git said on the way -- the same trick baseGoneMarker plays for
// prepareCheckout's own diagnosis, and the reason neither has to parse
// that framing.
const commitMarker = "grain-commit:"

// checkoutCommits lists the commits already on task's own branch and not on
// its base -- `git log <base>..HEAD` in the checkout prepareCheckout has
// just made, newest first, one "<abbrev> <subject>" per entry.
//
// This is the other half of what a redispatch needs to make sense of the
// branch it wakes up on (History, previousAttemptsSection): the store
// says how each earlier attempt ended, and this says what those attempts
// actually left behind. It is read here because here is the only place
// that has the checkout at all -- by the time BuildPrompt runs there is a
// string, not a repository.
//
// Not description.go's branchCommits, which asks GitHub for the same
// range over its API. That one runs at the end of a run, to describe a
// pull request and to decide whether there is anything to open one for,
// and needs an answer it can tell apart from a failed read. This one
// runs before the agent's first turn, in the sandbox, against a
// checkout that exists precisely because the run is about to work in
// it -- and treats every failure as nothing to say.
//
// Best effort, and never fatal: a missing base, a branch with nothing on
// it, a git that refuses for a reason nobody predicted, and a sandbox
// whose run_command is gone all come back as no commits. Orientation
// that could fail a dispatch would be a worse trade than the
// re-diagnosis it exists to save -- the commits are on the branch either
// way, and the agent has `git log` of its own.
//
// The base is resolved to whichever of origin/<task.Base> and origin/HEAD
// exists, in that order: a task with no base branched off whatever the
// clone left at origin/HEAD (prepareCheckout), and a task whose base has
// since been merged and deleted still has a branch worth describing --
// the same survivable case baseCheck carries on through.
func checkoutCommits(ctx context.Context, tools []mcp.Tool, task model.Task, limit int) []string {
	branch := task.BranchName()
	if !gitSafe.MatchString(branch) {
		return nil
	}
	refs := []string{"origin/HEAD"}
	if task.Base != "" && gitSafe.MatchString(task.Base) {
		refs = []string{"origin/" + task.Base, "origin/HEAD"}
	}
	run, ok := runCommandTool(tools)
	if !ok {
		return nil
	}
	script := fmt.Sprintf(`cd '%s' || exit 0
for ref in %s; do
  if git rev-parse --verify --quiet "$ref^{commit}" >/dev/null 2>&1; then
    git log --format='%s %%h %%s' -n %d "$ref..HEAD" 2>/dev/null
    exit 0
  fi
done`, CheckoutDir, "'"+strings.Join(refs, "' '")+"'", commitMarker, limit)

	result := run(ctx, map[string]any{"command": script})
	if result.IsError {
		log.Printf("orchestrator: task %s: reading what earlier attempts left on %s: %s",
			task.ID, branch, strings.TrimSpace(result.Text))
		return nil
	}
	var commits []string
	for _, line := range strings.Split(result.Text, "\n") {
		if said, ok := strings.CutPrefix(strings.TrimSpace(line), commitMarker); ok {
			if said = strings.TrimSpace(said); said != "" {
				commits = append(commits, said)
			}
		}
	}
	return commits
}

// sandboxRootMarker prefixes the single line sandboxWorkingDir has the
// sandbox print, so the answer can be picked back out of a run_command
// result that also carries the tool's own `exit=`/`stdout:`/`stderr:`
// framing -- the same trick stateRepoMarker and commitMarker play, and
// the reason this parses none of that framing.
const sandboxRootMarker = "grain-sandbox-root:"

// sandboxRootEcho is the one line that asks a sandbox where its own
// tools land: the absolute path run_command starts a command in, which
// is the directory everything else prepareCheckout does hangs off. It
// rides along at the top of the clone script rather than being asked for
// separately, so a dispatch that clones spends no extra round trip on it
// -- and leaves no extra entry in the narration setupNotes keeps on the
// task's own row.
func sandboxRootEcho() string {
	return "echo '" + sandboxRootMarker + "'\"$(pwd)\""
}

// sandboxRootFrom reads that answer back out of a run_command result,
// ignoring the tool's own `exit=`/`stdout:`/`stderr:` framing and
// whatever git printed around it.
//
// "" for an answer that is not there or is not an absolute path, which
// leaves the prompt saying exactly what it said before any of this
// existed (sandboxDirsSection): a directory grain guessed at would be
// the same wrong sentence a run's own framework already gives it.
func sandboxRootFrom(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if said, ok := strings.CutPrefix(strings.TrimSpace(line), sandboxRootMarker); ok {
			if said = strings.TrimSpace(said); strings.HasPrefix(said, "/") {
				return said
			}
		}
	}
	return ""
}

// sandboxWorkingDir asks the same question on its own, for the dispatch
// with nothing to clone: a task with no repo attached still works
// somewhere, and is still told the wrong thing about where by its own
// framework.
//
// Best effort, and never fatal: a slot with no run_command tool and a
// command that fails both come back as "".
func sandboxWorkingDir(ctx context.Context, tools []mcp.Tool) string {
	run, ok := runCommandTool(tools)
	if !ok {
		return ""
	}
	result := run(ctx, map[string]any{"command": sandboxRootEcho()})
	if result.IsError {
		log.Printf("orchestrator: asking the sandbox for its working directory: %s",
			strings.TrimSpace(result.Text))
		return ""
	}
	return sandboxRootFrom(result.Text)
}

// stateRepoMarker is what stateRepoCheck prints when the checkout turns
// out to be a grain state repository, picked back out of run_command's
// own framing the same way baseGoneMarker and commitMarker are.
const stateRepoMarker = "grain-state-repo:yes"

// stateRepoCheck is the shell that decides checkout.StateRepo: a
// tables/ directory beside a schema-version stamp is a dump written by
// pkg/staterepo and nothing else -- Export writes both, and HasDump asks
// the same question of a working tree the daemon owns.
//
// Run in the checkout, after the branch is in place, and deliberately
// unable to fail the dispatch: it ends in a bare `true` so that a tree
// which is not a state repository leaves the script's exit status at 0
// under `set -e`. What it decides is a paragraph of prompt; a missing
// paragraph is worth nothing next to a dispatch that did not happen.
func stateRepoCheck() string {
	return "if [ -d tables ] && [ -f schema-version ]; then echo '" + stateRepoMarker + "'; fi\ntrue"
}

// baseGoneMarker prefixes what baseCheck prints when a task's base is not
// on the remote, so prepareCheckout can pick its own words back out of a
// command's output and log them (there is nowhere else for the surviving
// case's diagnosis to go) without also logging git's.
const baseGoneMarker = "grain:"

// baseCheck is the shell that runs before either checkout arm and reports
// a base branch that is not on the remote.
//
// It is checked on both arms, not only the one that uses it. A base that
// is simply gone is the ordinary end of a branch's life -- it merged, and
// GitHub deleted it -- and a task can easily outlive one: New task
// prefills Base from the repo's last task (bwsalmon/agents#641), so a
// branch that merges between one task being filed and the next being
// dispatched leaves a task pointed at nothing. Checking only the arm that
// checks the base out meant that from the second attempt onward -- once
// the run's own branch was on the remote -- nothing looked at the base
// again until GitHub refused the pull request at the very end, with a 422
// nobody saw.
//
// What it does about it differs by arm, and that is the point:
//
//   - No branch on the remote yet: fatal. The base is what a fresh branch
//     would be rooted on, no work exists to lose, and a human retargeting
//     the task is cheap here and never gets cheaper. git's own answer
//     ("error: pathspec 'x' did not match any file(s) known to git")
//     names neither the base nor the repo it was looked for in and reads
//     like a corrupt checkout, so the message says which branch and
//     where.
//   - The branch is already there: not fatal. The base is not used on
//     this arm at all -- the branch's own history is what is continued --
//     and failing here would strand commits that are already pushed:
//     RunDispatch never reaches the agent, so runOne never salvages the
//     branch, and the task that was one refused pull request away from
//     finishing gets nothing at all instead. It says what it found and
//     carries on; EnsurePullRequest retargets at the default branch when
//     the run ends, and says so on the task.
func baseCheck(task model.Task, branch string) string {
	if task.Base == "" {
		return ""
	}
	return fmt.Sprintf(`if ! git rev-parse --verify --quiet 'refs/remotes/origin/%[1]s' >/dev/null; then
  echo "%[4]s base branch '%[1]s' does not exist on %[2]s -- it may have been merged and deleted; retarget this task at a branch that exists" >&2
  if ! git rev-parse --verify --quiet 'refs/remotes/origin/%[3]s' >/dev/null; then
    exit 1
  fi
  echo "%[4]s continuing '%[3]s', which is already on the remote; this task's pull request will open against the repository's default branch instead" >&2
fi
`, task.Base, task.Target, branch, baseGoneMarker)
}

// baseGoneError is baseCheck's fatal arm, kept apart from every other
// clone failure so RunDispatch can stop on it (stopOnMissingBase) rather
// than leave the task queued for another attempt that will fail in the
// same place: a branch that merged and was deleted does not come back.
//
// Error() is word for word what the clone failure always read as, since
// it is what task_run.detail carries and what `grain get` shows.
type baseGoneError struct {
	Repo model.RepoRef
	Base string
	err  error
}

func (e *baseGoneError) Error() string { return e.err.Error() }
func (e *baseGoneError) Unwrap() error { return e.err }

// accessRefusedError is a clone the git proxy refused because the
// credential the task's team clones with cannot reach the repository --
// no credential at all, one GitHub rejects, or one it accepts and still
// will not show this repository (gitproxy.IsAccessRefusal). Like
// baseGoneError it is kept apart from every other clone failure so
// RunDispatch can stop on it (stopOnAccessRefused): access is granted on
// GitHub or in Team settings, never by trying again.
type accessRefusedError struct {
	Repo   model.RepoRef
	Output string
	err    error
}

func (e *accessRefusedError) Error() string { return e.err.Error() }
func (e *accessRefusedError) Unwrap() error { return e.err }

// baseGoneFatally reports whether a failed clone's output is baseCheck
// refusing to go on without base -- its first line printed and its
// second, the survivable arm's, not.
func baseGoneFatally(output, base string) bool {
	if base == "" {
		return false
	}
	return strings.Contains(output, fmt.Sprintf("%s base branch '%s' does not exist on", baseGoneMarker, base)) &&
		!strings.Contains(output, baseGoneMarker+" continuing '")
}

// runCommandTool picks the run_command handler out of the tools a slot's
// sandbox exposes, so prepareCheckout can call it the same way the agent
// itself would -- by name, through the same handler, with no second path
// into the sandbox to keep in step with the first.
func runCommandTool(tools []mcp.Tool) (func(context.Context, map[string]any) mcp.Result, bool) {
	for _, t := range tools {
		if t.Name == "run_command" && t.Handler != nil {
			return t.Handler, true
		}
	}
	return nil, false
}

// cloneAttempts is how many times RetryingCloneScript runs `git clone`
// in all, the first try included.
const cloneAttempts = 3

// RetryingCloneScript is the shell for `git clone --quiet url dir` that
// runs it again, after removing the partial dir, when it failed the way
// a dropped transport makes it fail -- the sandbox's `granule forward`
// session to the git proxy reconnecting mid-clone ends a clone with
// "transfer closed with outstanding read data remaining" and "early
// EOF" -- rather than failing the sandbox's whole setup on it. Any other
// failure (a refusal, a missing repository) exits at once with git's own
// status, and git's stderr from every attempt is passed through, so the
// callers that read the clone's output for a refusal still see it.
//
// url and dir are spliced in as given: the caller quotes them. The
// script needs only POSIX sh and is safe under `set -e`.
func RetryingCloneScript(url, dir string) string {
	return fmt.Sprintf(`grain_clone_try=1
while :; do
  grain_clone_err=$(git clone --quiet %[1]s %[2]s 2>&1 >/dev/null) && grain_clone_rc=0 || grain_clone_rc=$?
  [ -z "$grain_clone_err" ] || printf '%%s\n' "$grain_clone_err" >&2
  [ "$grain_clone_rc" -ne 0 ] || break
  case "$grain_clone_err" in
    *"early EOF"*|*"transfer closed"*|*"unexpected disconnect"*|*"RPC failed"*|*"remote end hung up"*|*"Connection reset"*|*"Failed to connect"*|*"Could not connect"*|*"Empty reply"*) ;;
    *) exit "$grain_clone_rc" ;;
  esac
  [ "$grain_clone_try" -lt %[3]d ] || exit "$grain_clone_rc"
  grain_clone_try=$((grain_clone_try + 1))
  echo "[grain] git clone failed on a dropped connection; retrying (attempt $grain_clone_try of %[3]d)" >&2
  rm -rf %[2]s
  sleep 2
done`, url, dir, cloneAttempts)
}
